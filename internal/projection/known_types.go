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
// type appears once with the Month from its last event.
func KnownTypes(events []domain.Event) []KnownType {
	var known []KnownType
	seen := make(map[string]struct{})

	for i := len(events) - 1; i >= 0; i-- {
		e := events[i].Normalize()
		if _, ok := seen[e.Type]; ok {
			continue
		}

		seen[e.Type] = struct{}{}
		known = append(known, KnownType{
			Type:          e.Type,
			LastUsedMonth: e.Month,
		})
	}

	return known
}
