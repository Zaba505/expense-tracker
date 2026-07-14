package projection

import (
	"errors"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// ErrUnknownAction reports that Fold was handed an action it does not know
// how to apply. The policy is to fail the fold rather than silently skip an
// event, because an ignored event would make the current state quietly wrong.
var ErrUnknownAction = errors.New("projection: unknown action")

// ErrUnknownDirection reports that Fold was handed a direction it does not
// know how to count. It is ErrUnknownAction's policy applied to the other
// field the fold keys on, and for the same reason: a direction no rollup
// counts is money the user recorded and the totals omit.
//
// It has to be caught here, at the offending event, rather than later when
// the totals are taken. A bad direction folds into a cell perfectly happily —
// it is just a map key — and a rollup that first met it downstream could only
// fail for the whole log, taking every good month with it. Failing during the
// fold names the event that is wrong, which in an append-only log is the only
// thing the owner can act on: they cannot edit it, so they have to be able to
// find it and append the event that corrects it.
var ErrUnknownDirection = errors.New("projection: unknown direction")

// Key identifies one cell of the monthly state: the amount for a specific
// month, type, and direction.
//
// Direction is part of the identity of a cell, not an attribute of it, so
// that money stays in the direction it was recorded with. The alternative —
// one cell per (month, type), carrying whichever direction the latest event
// happened to name — would let a single income event retroactively reclassify
// every expense already accumulated under that type, which is exactly the
// silent drift this project exists to remove. Here an event's amount lands in
// its own direction's cell and stays there, and a type recorded both ways in
// one month keeps both amounts instead of collapsing to the last writer.
//
// In practice a type is only ever one direction — "Groceries" is an expense,
// "Paycheck" is income — so this splits nothing that the user thinks of as
// whole. When it does split something, it is because the log really does say
// two different things, and rollups had better not average them away.
//
// The price is that ActionSet supersedes within a direction, not across one.
// An entry recorded in the wrong direction — the likely mistake, since expense
// is the default every path falls back to — is not corrected by setting it
// again in the right one: that fills the right cell and leaves the wrong one
// standing, and the month then counts the money twice, once on each side. It
// takes two events, the second retiring the cell that should not exist:
//
//	set (2026-07, "Paycheck", income)  = 5000.00  // where it belongs
//	set (2026-07, "Paycheck", expense) =    0.00  // and out of where it does not
//
// That is a correction the entry path owes the user (it knows the direction it
// is replacing); it is not one the fold can make on its own, because an event
// that silently reached across directions is exactly the retroactive
// reclassification this key exists to prevent.
type Key struct {
	Month     string
	Type      string
	Direction domain.Direction
}

// State is the current amount for each (month, type, direction) cell after
// replaying a sequence of events in order. It is what every read model is
// derived from: RollupByMonth sums it by direction, and the known-types and
// yearly-grid projections read its keys.
type State map[Key]money.Cents

type actionHandler func(State, domain.Event)

var handlers = map[domain.Action]actionHandler{
	domain.ActionAdd: func(state State, e domain.Event) {
		state[keyOf(e)] += e.Amount
	},
	domain.ActionSet: func(state State, e domain.Event) {
		state[keyOf(e)] = e.Amount
	},
}

// keyOf is the cell a normalized event lands in. Fold normalizes before it
// gets here, which is what makes the key canonical: an event with no direction
// is an expense (domain.Event.Normalize's rule), and keying one unnormalized
// would file it under the empty direction — a cell no rollup counts, so the
// amount would silently vanish from every total. The same goes for a stray
// space in Type, which would shadow the real type.
func keyOf(e domain.Event) Key {
	return Key{Month: e.Month, Type: e.Type, Direction: e.Direction}
}

// Fold replays events into the current monthly state.
//
// Fold is pure and order-respecting: it applies the input slice exactly as it
// is given, so callers that need log order must pass events already sorted in
// log order. Unknown or future actions abort the fold with ErrUnknownAction,
// and directions the rollups cannot count abort it with ErrUnknownDirection,
// rather than either being ignored.
//
// Fold normalizes each event before folding it, so the cells it produces are
// canonical no matter where the events came from. A store normalizes on the
// way out and validates what it loads, so its events arrive canonical already
// and this changes nothing. It is here for the events that never went through
// one — the importer's, a test's, a hand-built one — because those are exactly
// the events that can carry a direction nothing has vetted, and State is only
// as trustworthy as the keys it is built from.
func Fold(events []domain.Event) (State, error) {
	state := make(State)
	canonical, _, err := compileHistory(events)
	if err != nil {
		return nil, err
	}

	for _, e := range canonical {
		handle := handlers[e.Action]
		handle(state, e)
	}

	return state, nil
}
