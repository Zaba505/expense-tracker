package projection

import (
	"errors"
	"fmt"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// ErrUnknownAction reports that Fold was handed an action it does not know
// how to apply. The policy is to fail the fold rather than silently skip an
// event, because an ignored event would make the current state quietly wrong.
var ErrUnknownAction = errors.New("projection: unknown action")

// Key identifies one cell of the monthly state: the amount for a specific
// month and type.
type Key struct {
	Month string
	Type  string
}

// State is the current amount for each (month, type) pair after replaying a
// sequence of events in order.
type State map[Key]money.Cents

type actionHandler func(State, domain.Event)

var handlers = map[domain.Action]actionHandler{
	domain.ActionAdd: func(state State, e domain.Event) {
		key := Key{Month: e.Month, Type: e.Type}
		state[key] += e.Amount
	},
	domain.ActionSet: func(state State, e domain.Event) {
		state[Key{Month: e.Month, Type: e.Type}] = e.Amount
	},
}

// Fold replays events into the current monthly state.
//
// Fold is pure and order-respecting: it applies the input slice exactly as it
// is given, so callers that need log order must pass events already sorted in
// log order. Unknown or future actions abort the fold with ErrUnknownAction
// rather than being ignored.
func Fold(events []domain.Event) (State, error) {
	state := make(State)

	for _, e := range events {
		handle, ok := handlers[e.Action]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownAction, e.Action)
		}
		handle(state, e)
	}

	return state, nil
}
