package eventlog

import (
	"cmp"
	"context"
	"fmt"
	"iter"
	"slices"
	"sync"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
)

// Memory is an in-memory EventStore: the same append-only log, without a
// database under it.
//
// It exists so that everything downstream of the log — the fold, the
// projections, the handlers — can be tested against a real log at the
// speed of a map, and so those tests say something about the code that
// runs in production rather than about a mock. It enforces what Firestore
// enforces (no overwrites, defaults filled, the same total order), which
// is why both implementations are held to one conformance suite.
//
// It is not a cache and not a fallback: nothing wires it into the running
// app. Data lives as long as the process does.
//
// A Memory is safe for concurrent use, and its zero value is an empty log
// ready to append to.
type Memory struct {
	mu     sync.RWMutex
	events []domain.Event
	ids    map[string]struct{}
	seq    int64
}

// NewMemory returns an empty in-memory log.
func NewMemory() *Memory {
	return &Memory{}
}

// Append records e and returns it as stored. It implements EventStore.
//
// The event is copied in, so a caller that goes on to reuse the value it
// passed cannot reach back and change what the log says — the same
// isolation a database gives for free, and the reason a test against
// Memory can trust what it reads back.
func (m *Memory) Append(ctx context.Context, e domain.Event) (domain.Event, error) {
	// Validate before consulting the context, which is the order the
	// Firestore store takes for free: it cannot learn that a write was
	// cancelled until it has an event to try to write. A bad event is bad
	// whether or not the caller is still waiting to hear about it, and a
	// handler that maps ErrInvalidEvent to 400 should not get a different
	// answer here than it gets in production.
	e, err := prepare(e, time.Now)
	if err != nil {
		return domain.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Event{}, fmt.Errorf("eventlog: appending event: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	e.ID = m.nextID()
	m.store(e)
	return e, nil
}

// AppendUnique records e under key unless key is taken, and returns
// ErrDuplicateKey if it is. It implements UniqueAppender.
//
// The key is the event's ID, exactly as in the Firestore store — where the
// key is the document's name — so an importer that has already imported a
// row finds it here the same way it finds it there, and a test of a re-run
// is a test of the re-run that happens in production.
func (m *Memory) AppendUnique(ctx context.Context, key string, e domain.Event) (domain.Event, error) {
	// The key and the event before the context, for the reason Append
	// takes them in that order: a bad argument is bad whether or not the
	// caller is still waiting to hear about it.
	e, err := prepareKeyed(key, e, time.Now)
	if err != nil {
		return domain.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Event{}, fmt.Errorf("eventlog: appending event: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Checked under the same lock that takes the key, not before it: two
	// goroutines appending the same key must not both find it free. That is
	// what Firestore's Create gives for free, and Memory is only worth
	// testing an importer against if it gives it too.
	if _, taken := m.ids[key]; taken {
		return domain.Event{}, fmt.Errorf("%w: %s", ErrDuplicateKey, key)
	}

	e.ID = key
	m.store(e)
	return e, nil
}

// nextID returns an unused sequence ID. It is called with the lock held.
//
// Zero-padded so that the IDs sort lexicographically, which is all the
// load order asks of them: it breaks ties on RecordedAt by ID, and only
// needs that comparison to be total and stable.
//
// It happens to make ties fall in append order here, where Firestore's
// random IDs make them fall arbitrarily. Do not build on that — a test
// that depends on it is a test that passes against Memory and fails in
// production. EventStore.Load says what may be relied on.
//
// The loop skips whatever a keyed append has already claimed. Nothing in
// this app would hand AppendUnique a key that looks like one of these —
// the importer's are hashes — but Firestore would refuse the collision
// rather than overwrite, and a Memory that quietly kept two events under
// one ID would have stopped standing in for it.
func (m *Memory) nextID() string {
	for {
		m.seq++
		id := fmt.Sprintf("%020d", m.seq)
		if _, taken := m.ids[id]; !taken {
			return id
		}
	}
}

// store records an event and its ID. It is called with the lock held, and
// lazily builds the ID set, so the zero Memory is still a log ready to be
// appended to.
func (m *Memory) store(e domain.Event) {
	if m.ids == nil {
		m.ids = make(map[string]struct{})
	}
	m.ids[e.ID] = struct{}{}
	m.events = append(m.events, e)
}

// Load streams the log in the log's total order — RecordedAt, then ID. It
// implements EventStore.
//
// The order is computed on each call rather than maintained on append:
// events arrive in whatever order they were appended, but a caller may
// supply a RecordedAt (the importer does), so append order and log order
// are not the same thing. Sorting a snapshot is the honest way to say
// that, and it costs nothing at the sizes a single user's log reaches.
func (m *Memory) Load(ctx context.Context) iter.Seq2[domain.Event, error] {
	return func(yield func(domain.Event, error) bool) {
		// Before the first event and not only between them. The Firestore
		// store starts by asking the database, so a cancelled context
		// fails there whether the log has events in it or not — and an
		// empty log is the one case where a loop-only check would find
		// nothing to check, and quietly hand a cancelled fold a clean
		// "there are no expenses".
		if err := ctx.Err(); err != nil {
			yield(domain.Event{}, fmt.Errorf("eventlog: loading events: %w", err))
			return
		}

		// Snapshot when the range starts, not when Load is called, and
		// under the read lock: a fold iterates while the web handler that
		// triggered it may still be appending, and a fold over a log that
		// grew underneath it is a fold over a log that never existed.
		m.mu.RLock()
		events := slices.Clone(m.events)
		m.mu.RUnlock()

		slices.SortFunc(events, func(a, b domain.Event) int {
			if c := a.RecordedAt.Compare(b.RecordedAt); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})

		for _, e := range events {
			// Checked every step too: the Firestore store abandons its
			// stream when the context is cancelled mid-fold, so this one
			// has to as well, or a consumer's cancellation would mean
			// different things in a test and in production.
			if err := ctx.Err(); err != nil {
				yield(domain.Event{}, fmt.Errorf("eventlog: loading events: %w", err))
				return
			}
			if !yield(e, nil) {
				return
			}
		}
	}
}
