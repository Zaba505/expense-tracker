package projection

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// TypeRenamePreview is what renaming one type into another would do to the log
// as it currently stands: which cells it would move, and what each of them
// would come to afterwards.
//
// It is the answer to a question the owner must be able to ask before they
// append an event they cannot take back, so it is computed the only way that
// cannot lie about it — by folding the log twice, once as it is and once with
// the rename applied, and reporting the difference.
type TypeRenamePreview struct {
	FromType string
	ToType   string

	// AffectedEntries is how many log events would come to be read under
	// ToType instead of FromType.
	AffectedEntries int

	// Cells is every cell the rename would move, oldest month first.
	Cells []TypeRenameCell

	// Conflicts reports whether any cell would lose money — see
	// TypeRenameCell.Conflict. A rename that conflicts is refused rather
	// than applied.
	Conflicts bool
}

// TypeRenameCell is one (month, direction) the rename would move, with the
// money on each side of it.
//
// Direction is part of it because it is part of a cell's identity (see Key):
// money never crosses from expense to income, so a rename cannot merge across
// one either, and a type recorded both ways in one month has two cells to
// account for rather than one.
type TypeRenameCell struct {
	Month     string
	Direction domain.Direction

	// Entries is how many of the log's events in this cell belong to FromType.
	Entries int

	// From and To are what the two types currently come to here, and Result is
	// what the target type would come to once the rename folds them together.
	From   money.Cents
	To     money.Cents
	Result money.Cents
}

// Conflict reports whether this cell would lose money to the rename.
//
// It is the whole safety property, and it is worth being precise about why it
// can fail at all. Two adds merge by summing, and that is what Result comes to,
// so adds never conflict. ActionSet does not sum — the last set wins — so two
// types that both carry a set in the same cell state two different totals for
// what the rename would make one type, and folding them can only keep the later
// one. $40 of "Fuel" merged into $10 of "Gas" is then $10, and $40 the owner
// recorded is simply gone from the month.
//
// The log cannot resolve that on its own: both events are true statements about
// the totals of two types, and nothing in them says what the total of the
// merged type should be. Only the owner knows, so a conflicting rename is
// refused and they are shown the months to settle first — with an explicit set,
// which is the event that says "this is the total" and is exactly what is
// missing.
//
// Set events are what the spreadsheet import writes, so this is the ordinary
// path for merging two imported types, not a corner of one.
func (c TypeRenameCell) Conflict() bool { return c.Result != c.From+c.To }

// Months is how many distinct months the rename would touch.
func (p TypeRenamePreview) Months() int {
	months := make(map[string]struct{}, len(p.Cells))
	for _, cell := range p.Cells {
		months[cell.Month] = struct{}{}
	}
	return len(months)
}

// canonicalize replays the log's renames into the history they rename, and
// returns the add and set events as that history now reads — which is what
// every projection folds, so that a rename reaches every month at once without
// a single stored event being rewritten.
//
// A rename rewrites only what came before it. That is what keeps a name usable:
// an entry recorded after "Fuel" was renamed to "Gas" is a new Fuel, not more
// Gas, and can be corrected or renamed in its own right. An alias applied to
// the whole log — later events included — would retire the old name for good,
// silently absorbing every future use of a name the type field still accepts,
// and would make a rename irreversible: renaming Gas back to Fuel would resolve
// both sides to Gas and do nothing. Scoping the rename to the past leaves the
// log's own order as the only thing deciding what a name means, and when.
//
// It is O(n) per rename rather than O(1), because it rewrites history in place
// instead of carrying an alias table forward. Renames are rare — a typo cleanup
// is not an hourly event — and this way there is no table to get stale, no
// chain to follow, and no cycle to break.
func canonicalize(events []domain.Event) ([]domain.Event, error) {
	var canonical []domain.Event

	for i, raw := range events {
		e := raw.Normalize()

		switch e.Action {
		case domain.ActionAdd, domain.ActionSet:
			if !e.Direction.Valid() {
				return nil, fmt.Errorf("%w: %q (event %d, id %q, month %q, type %q)", ErrUnknownDirection, e.Direction, i, e.ID, e.Month, e.Type)
			}
			canonical = append(canonical, e)
		case domain.ActionRenameType:
			renameType(canonical, e.Type, e.ToType)
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnknownAction, e.Action)
		}
	}

	return canonical, nil
}

// renameType rewrites, in place, the history recorded so far.
//
// A rename that names no type or renames a type to itself is a no-op rather
// than an error: domain.Event.Validate already refuses to let one into the log,
// so meeting one here means it came from somewhere that never went through the
// store, and there is nothing about it to apply either way.
func renameType(canonical []domain.Event, from, to string) {
	if from == "" || to == "" || from == to {
		return
	}

	for i := range canonical {
		if canonical[i].Type == from {
			canonical[i].Type = to
		}
	}
}

// PreviewTypeRename reports what renaming or merging fromType into toType would
// do to the log as it stands: the cells it would move, the entries in each, and
// the money on both sides of every one of them.
//
// It folds the log twice — as it is, and with the rename applied — and reports
// the difference, so what it promises is what a fold after the append would
// actually produce. A preview derived from counting events instead would be
// cheaper and would miss the only thing worth previewing: that a merge can
// destroy money (see TypeRenameCell.Conflict).
func PreviewTypeRename(events []domain.Event, fromType, toType string) (TypeRenamePreview, error) {
	canonical, err := canonicalize(events)
	if err != nil {
		return TypeRenamePreview{}, err
	}

	preview := TypeRenamePreview{
		FromType: strings.TrimSpace(fromType),
		ToType:   strings.TrimSpace(toType),
	}
	if preview.FromType == "" || preview.ToType == "" || preview.FromType == preview.ToType {
		return preview, nil
	}

	before, err := foldCanonical(canonical)
	if err != nil {
		return TypeRenamePreview{}, err
	}

	renamed := slices.Clone(canonical)
	renameType(renamed, preview.FromType, preview.ToType)
	after, err := foldCanonical(renamed)
	if err != nil {
		return TypeRenamePreview{}, err
	}

	// A cell only moves if the source type has events in it, so the source's
	// own events are the whole list of what to report on.
	type cell struct {
		month     string
		direction domain.Direction
	}
	entries := make(map[cell]int)
	for _, e := range canonical {
		if e.Type != preview.FromType {
			continue
		}
		entries[cell{month: e.Month, direction: e.Direction}]++
		preview.AffectedEntries++
	}

	for c, count := range entries {
		moved := TypeRenameCell{
			Month:     c.month,
			Direction: c.direction,
			Entries:   count,
			From:      before[Key{Month: c.month, Type: preview.FromType, Direction: c.direction}],
			To:        before[Key{Month: c.month, Type: preview.ToType, Direction: c.direction}],
			Result:    after[Key{Month: c.month, Type: preview.ToType, Direction: c.direction}],
		}
		if moved.Conflict() {
			preview.Conflicts = true
		}
		preview.Cells = append(preview.Cells, moved)
	}

	slices.SortFunc(preview.Cells, func(a, b TypeRenameCell) int {
		return cmp.Or(
			cmp.Compare(a.Month, b.Month),
			cmp.Compare(a.Direction, b.Direction),
		)
	})

	return preview, nil
}
