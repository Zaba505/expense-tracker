package projection

import "github.com/Zaba505/expense-tracker/internal/domain"

// KnownType is one distinct type the log has mentioned, labelled with the most
// recent month any event recorded it against.
type KnownType struct {
	Type          string
	LastUsedMonth string
}

// KnownTypes returns the log's distinct types, ordered by how recently the log
// mentioned each one.
//
// It is pure and order-respecting like Fold: callers that need log recency
// must pass events already in log order. The result is newest first, and each
// type appears once with the Month from its last event. A type the log has
// renamed away is gone from the list, and the type it became carries the month
// of whichever event mentioned it last.
//
// It fails for the events Fold fails on, and for the same reason: it reads the
// log through the same canonicalization, so an event Fold cannot replay is one
// this cannot name a type from either. Returning an empty list instead would
// answer a broken log with a blank autocomplete and no way to tell that apart
// from a log with nothing in it.
func KnownTypes(events []domain.Event) ([]KnownType, error) {
	canonical, err := canonicalize(events)
	if err != nil {
		return nil, err
	}

	var known []KnownType
	seen := make(map[string]struct{})

	for i := len(canonical) - 1; i >= 0; i-- {
		e := canonical[i]
		if _, ok := seen[e.Type]; ok {
			continue
		}

		seen[e.Type] = struct{}{}
		known = append(known, KnownType{
			Type:          e.Type,
			LastUsedMonth: e.Month,
		})
	}

	return known, nil
}
