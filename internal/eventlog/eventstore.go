package eventlog

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"
	"unicode/utf8"

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

// UniqueAppender is the log plus one more way in: an append that carries
// the caller's own name for the fact it is appending, and that refuses to
// append the same fact twice.
//
// It is a second interface rather than two more lines on EventStore
// because only one caller has any business with it. The web handlers take
// EventStore, and what they can do to the log is exactly what that
// interface says they can — appending what the owner just typed. Replaying
// a five-year-old spreadsheet row under a key of the caller's choosing is
// the importer's job and nobody else's, and an interface is the cheapest
// place to say so: a handler cannot call a method it was never handed.
//
// Both implementations satisfy it, and the conformance suite holds both to
// it, for the reason EventStore's own suite exists — an idempotence that
// only Memory has is an idempotence the import does not have.
type UniqueAppender interface {
	EventStore

	// AppendUnique writes e to the log under key, unless the log already
	// holds an event under that key — in which case nothing is written and
	// the error is ErrDuplicateKey.
	//
	// It exists because an append-only log cannot take a write back. The
	// importer replays five years of a spreadsheet, and a re-run — after a
	// crash, a half-finished import, a network that dropped in the middle —
	// must not append a second copy of every row it already appended. The
	// key is the caller's name for the fact rather than the store's: the
	// importer derives one per source row, so "have I imported this row"
	// has an answer that outlives the process that asked.
	//
	// The key becomes the event's ID. That is deliberate, and it is what
	// keeps the guarantee honest: the record of what has been imported is
	// the log itself, not a second collection of keys beside it that a
	// half-finished run could leave disagreeing with the events.
	//
	// The check is atomic with the write, not a read the caller does first.
	// Two importers racing must not both find the key absent and both
	// append — and no caller can get that by looking before it leaps.
	//
	// key must be able to name a document: non-empty, valid UTF-8, no
	// slash, not "." or "..", not "__…__", and at most MaxKeyLen bytes.
	// Anything else is ErrInvalidKey. Every other rule Append obeys — the
	// defaults it fills in, the events it refuses — applies here unchanged.
	AppendUnique(ctx context.Context, key string, e domain.Event) (domain.Event, error)
}

// ErrEventAppended is returned by Append when the event it is handed
// already carries an ID — that is, when it has already been appended.
// Appending it again would either duplicate the fact or, if the store
// honored the ID, edit history in place. Both are the thing this log
// exists to prevent, so it is refused.
var ErrEventAppended = errors.New("eventlog: event already has an ID; the log is append-only")

// ErrDuplicateKey is returned by AppendUnique when the log already holds
// an event under the key it was given.
//
// It is not a failure. An importer re-running over rows it has already
// imported expects it for every one of them, and getting it for every one
// of them is what a safe re-run looks like — so callers have to tell it
// apart from a write that actually went wrong, with errors.Is, rather than
// reading any error out of a batch of appends as a broken import.
var ErrDuplicateKey = errors.New("eventlog: an event with this key is already in the log")

// ErrInvalidKey is returned by AppendUnique when the key it was given
// cannot name a document.
var ErrInvalidKey = errors.New("eventlog: invalid key")

// MaxKeyLen is the longest key AppendUnique accepts, in bytes: Firestore's
// limit on a document ID.
const MaxKeyLen = 1500

var (
	_ EventStore = (*Store)(nil)
	_ EventStore = (*Memory)(nil)

	_ UniqueAppender = (*Store)(nil)
	_ UniqueAppender = (*Memory)(nil)
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

// prepareKeyed is prepare for the keyed write path: the key has to be
// good before the event is worth looking at, since a key that cannot name
// a document is a caller bug rather than a bad row.
func prepareKeyed(key string, e domain.Event, now func() time.Time) (domain.Event, error) {
	if err := checkKey(key); err != nil {
		return domain.Event{}, err
	}
	return prepare(e, now)
}

// checkKey reports whether key can name a document in the events
// collection.
//
// The rules are Firestore's, and Memory is held to every one of them —
// that is the point of two implementations behind one interface, and it is
// load-bearing here, because Firestore does not refuse these keys
// politely. A slash does not fail: it addresses a subcollection, so the
// event lands at a path the log's queries never look at, and an import
// that reported success would be missing rows nothing can find. A "__x__"
// key collides with the namespace Firestore reserves for itself. Each of
// them is a write that cannot be taken back, so each of them is refused
// before it is attempted, in the one place both stores go through.
func checkKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%w: the key is empty", ErrInvalidKey)
	case len(key) > MaxKeyLen:
		return fmt.Errorf("%w: the key is %d bytes, the limit is %d", ErrInvalidKey, len(key), MaxKeyLen)
	case !utf8.ValidString(key):
		return fmt.Errorf("%w: the key is not valid UTF-8", ErrInvalidKey)
	case strings.Contains(key, "/"):
		return fmt.Errorf("%w: the key %q has a slash in it, which would address a subcollection rather than name a document", ErrInvalidKey, key)
	case key == "." || key == "..":
		return fmt.Errorf("%w: the key %q is a path element, not a name", ErrInvalidKey, key)
	case strings.HasPrefix(key, "__") && strings.HasSuffix(key, "__"):
		return fmt.Errorf("%w: the key %q is in the namespace Firestore reserves for itself", ErrInvalidKey, key)
	}
	return nil
}
