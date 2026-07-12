package eventlog

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
)

// EventStore is the append-only event log. Everything the app knows is
// derived by folding what Load yields; everything it learns is appended
// through Append. There is deliberately no Update and no Delete: the
// interface is the guarantee, not just the implementation of it.
//
// Two implementations satisfy it — the Firestore-backed Store, and Memory
// for tests and for folds over an event set that has no database behind
// it. Consumers (projections, the web handlers) take this interface, so
// none of them can reach past the log into Firestore.
//
// Implementations are safe for concurrent use.
type EventStore interface {
	// Append writes e to the log and returns it as stored: with the ID
	// the store assigned, and with the defaults the store filled in.
	//
	// The event that comes back is the event that was written. Callers
	// that want to know what a fold will see should read it rather than
	// the value they passed in — RecordedAt in particular is the store's
	// to assign when the caller leaves it zero.
	//
	// Append never overwrites: an event with an ID already set is a
	// caller trying to write to a document of their choosing, and is
	// refused with ErrEventAppended rather than honored.
	Append(ctx context.Context, e domain.Event) (domain.Event, error)

	// Load streams the whole log in the log's total order: RecordedAt
	// ascending, then ID ascending to break ties. The order is what makes
	// a fold reproducible — the same events must reduce to the same state
	// on every instance, on every load, or two replicas would disagree
	// about the same month.
	//
	// The tiebreak is total and stable, but it is not meaningful: IDs are
	// opaque, and Firestore's are random, so which of two events sharing a
	// RecordedAt sorts first is arbitrary — settled once, when they were
	// written, and the same on every load thereafter. Nothing may read
	// "the later append wins" into it. An event that has to supersede
	// another one (a set correcting a set) must carry a strictly later
	// RecordedAt, which is automatic when a store stamps the clock and is
	// the caller's job when the caller supplies it — the importer's,
	// replaying a sheet.
	//
	// The sequence yields a zero Event with a non-nil error at most once,
	// and stops there: a partially-read log is not a smaller log, it is a
	// wrong one, so a consumer that folds must abort on the first error
	// rather than skip past it.
	//
	// Stopping early (a break in the range) releases the store's side of
	// the stream. The context bounds the whole stream, not just its
	// first page.
	Load(ctx context.Context) iter.Seq2[domain.Event, error]
}

// ErrEventAppended is returned by Append when the event it is handed
// already carries an ID — that is, when it has already been appended.
// Appending it again would either duplicate the fact or, if the store
// honored the ID, edit history in place. Both are the thing this log
// exists to prevent, so it is refused.
var ErrEventAppended = errors.New("eventlog: event already has an ID; the log is append-only")

var (
	_ EventStore = (*Store)(nil)
	_ EventStore = (*Memory)(nil)
)

// prepare is the write path every store shares: it turns a caller's event
// into the event that will be written, or explains why it will not be.
//
// Both stores run it, and that is the point — the defaults and the
// refusals are properties of the log, not of the database behind it, so
// an event Memory accepts in a test is one Firestore accepts in
// production, and one it rejects is one Firestore rejects too.
//
// now supplies RecordedAt when the caller left it zero. The caller may
// set it instead: the importer does, so that five years of replayed
// history keep the order they originally happened in rather than all
// landing at the instant of the import.
func prepare(e domain.Event, now func() time.Time) (domain.Event, error) {
	if e.ID != "" {
		return domain.Event{}, fmt.Errorf("%w: %s", ErrEventAppended, e.ID)
	}

	if e.RecordedAt.IsZero() {
		e.RecordedAt = now()
	}

	// Normalize after stamping the clock, not before: it is what moves a
	// timestamp to UTC and down to the log's resolution, and the one this
	// store just took needs that as much as one a caller supplied.
	e = e.Normalize()

	if err := e.Validate(); err != nil {
		return domain.Event{}, err
	}
	return e, nil
}
