package projection

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Zaba505/expense-tracker/internal/domain"
)

// TypeRenamePreview is the current log's impact summary for renaming one type
// to another.
type TypeRenamePreview struct {
	FromType        string
	ToType          string
	AffectedEntries int
	Months          []TypeRenameMonth
}

// TypeRenameMonth is one month the rename would touch, with the number of log
// events in that month that currently belong to the source type.
type TypeRenameMonth struct {
	Month   string
	Entries int
}

type typeAliases map[string]string

func canonicalizeHistory(events []domain.Event) ([]domain.Event, typeAliases, error) {
	aliases, err := collectTypeAliases(events)
	if err != nil {
		return nil, nil, err
	}
	return canonicalEvents(events, aliases)
}

func collectTypeAliases(events []domain.Event) (typeAliases, error) {
	aliases := make(typeAliases)

	for _, raw := range events {
		e := raw.Normalize()

		switch e.Action {
		case domain.ActionAdd, domain.ActionSet:
			continue
		case domain.ActionRenameType:
			from := aliases.resolve(e.Type)
			to := aliases.resolve(e.ToType)
			if from == "" || to == "" || from == to {
				continue
			}
			aliases[from] = to
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnknownAction, e.Action)
		}
	}

	return aliases, nil
}

func canonicalEvents(events []domain.Event, aliases typeAliases) ([]domain.Event, typeAliases, error) {
	var canonical []domain.Event

	for i, raw := range events {
		e := raw.Normalize()

		switch e.Action {
		case domain.ActionRenameType:
			continue
		case domain.ActionAdd, domain.ActionSet:
			if !e.Direction.Valid() {
				return nil, nil, fmt.Errorf("%w: %q (event %d, id %q, month %q, type %q)", ErrUnknownDirection, e.Direction, i, e.ID, e.Month, e.Type)
			}
			e.Type = aliases.resolve(e.Type)
			canonical = append(canonical, e)
		default:
			return nil, nil, fmt.Errorf("%w: %q", ErrUnknownAction, e.Action)
		}
	}

	return canonical, aliases, nil
}

// resolve follows a type's alias chain to the current canonical name and caches
// the result back onto every intermediate alias it passed through, so repeated
// lookups do not have to traverse the same chain again.
func (aliases typeAliases) resolve(typ string) string {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return ""
	}

	// Follow the alias chain all the way to the current canonical type while
	// remembering the intermediate names we passed through.
	var path []string
	cur := typ
	for {
		next, ok := aliases[cur]
		if !ok || next == "" || next == cur {
			break
		}
		path = append(path, cur)
		cur = next
	}
	// Cache that final answer back onto every intermediate alias we saw, so the
	// next lookup for any of them resolves in one map read instead of walking the
	// whole chain again.
	for _, seen := range path {
		aliases[seen] = cur
	}
	return cur
}

// PreviewTypeRename reports which current months and entries would be affected
// if the source type were renamed or merged into the target type.
func PreviewTypeRename(events []domain.Event, fromType, toType string) (TypeRenamePreview, error) {
	canonical, aliases, err := canonicalizeHistory(events)
	if err != nil {
		return TypeRenamePreview{}, err
	}

	preview := TypeRenamePreview{
		FromType: aliases.resolve(fromType),
		ToType:   aliases.resolve(toType),
	}
	if preview.FromType == "" || preview.ToType == "" || preview.FromType == preview.ToType {
		return preview, nil
	}

	counts := make(map[string]int)
	for _, e := range canonical {
		if e.Type != preview.FromType {
			continue
		}
		counts[e.Month]++
		preview.AffectedEntries++
	}

	for month, entries := range counts {
		preview.Months = append(preview.Months, TypeRenameMonth{Month: month, Entries: entries})
	}
	slices.SortFunc(preview.Months, func(a, b TypeRenameMonth) int {
		return cmp.Compare(a.Month, b.Month)
	})

	return preview, nil
}
