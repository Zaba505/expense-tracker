package eventlog

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// Collection names. The event log is the single source of truth and lives
// in EventsCollection; MetaCollection holds the store's own bookkeeping —
// documents the app writes about itself, never domain events.
const (
	EventsCollection = "events"
	MetaCollection   = "meta"

	// HealthDoc is the meta document the readiness check round-trips. It
	// is a fixed path rather than a fresh document per check so a probe
	// running every few seconds cannot grow the database without bound.
	HealthDoc = "health"
)

// Options configures a Store.
type Options struct {
	// ProjectID is the Google Cloud project owning the Firestore
	// database. Required, even against the emulator, since it namespaces
	// the emulated data.
	ProjectID string

	// EmulatorHost, when non-empty, is the host:port of a local Firestore
	// emulator to use instead of the live service. Empty means "live
	// Firestore, authenticated with Application Default Credentials".
	EmulatorHost string
}

// Store is the Firestore-backed event log. It owns the Firestore client
// and must be closed.
//
// A Store is safe for concurrent use; the underlying Firestore client is.
type Store struct {
	client *firestore.Client
	events *firestore.CollectionRef
	health *firestore.DocumentRef
}

// New connects to Firestore: to the live service via Application Default
// Credentials, or — when opts.EmulatorHost is set — to a local emulator.
//
// It does not dial: gRPC connects lazily on the first call, and both
// paths are cheap and non-blocking. Whether the database is actually
// reachable is what Check answers.
func New(ctx context.Context, opts Options) (*Store, error) {
	if opts.ProjectID == "" {
		return nil, errors.New("eventlog: ProjectID is required")
	}

	var clientOpts []option.ClientOption
	var conn *grpc.ClientConn

	if opts.EmulatorHost != "" {
		// The Firestore client sniffs FIRESTORE_EMULATOR_HOST out of the
		// process environment on its own, but dialing explicitly keeps the
		// emulator decision where every other environment decision in this
		// app lives — config.Load — instead of splitting it across two
		// readers of the same variable that could disagree.
		//
		// The emulator ignores credentials but the wire still needs some:
		// "Bearer owner" is the token the client library sends, and the
		// same token keeps the emulator's security rules happy.
		c, err := grpc.NewClient(opts.EmulatorHost,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithPerRPCCredentials(emulatorCreds{}),
		)
		if err != nil {
			return nil, fmt.Errorf("eventlog: emulator at %s: %w", opts.EmulatorHost, err)
		}
		conn = c
		clientOpts = append(clientOpts, option.WithGRPCConn(conn))
	}

	client, err := firestore.NewClient(ctx, opts.ProjectID, clientOpts...)
	if err != nil {
		// Only this path has to clean the connection up: on success the
		// Firestore client adopts it (see Close).
		if conn != nil {
			_ = conn.Close()
		}
		return nil, fmt.Errorf("eventlog: firestore client for project %s: %w", opts.ProjectID, err)
	}

	return &Store{
		client: client,
		events: client.Collection(EventsCollection),
		health: client.Collection(MetaCollection).Doc(HealthDoc),
	}, nil
}

// Append writes e as a new document in the events collection and returns
// it as stored. It implements EventStore.
//
// Immutability is structural rather than promised. The document ID is
// generated here, so no caller can name a document to overwrite, and the
// write is a Create rather than a Set: were an ID ever to collide,
// Firestore would refuse the write instead of replacing what is there.
// Nothing in this package issues an Update or a Delete against the
// collection — the only way to change what the log says is to append to
// it.
//
// That holds for callers who go through this package, which is every
// caller in this binary. It is not yet enforced by the database: making
// Firestore itself reject an update to an existing event belongs in the
// security rules, which the deploy story writes.
func (s *Store) Append(ctx context.Context, e domain.Event) (domain.Event, error) {
	e, err := prepare(e, time.Now)
	if err != nil {
		return domain.Event{}, err
	}

	ref := s.events.NewDoc()
	if _, err := ref.Create(ctx, newEventDoc(e)); err != nil {
		// As in Load: the gRPC status for a cancelled write says
		// "Canceled" but does not unwrap to context.Canceled, and a caller
		// that gave up needs to read differently from a database that
		// fell over.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.Event{}, fmt.Errorf("eventlog: appending event: %w", ctxErr)
		}
		return domain.Event{}, fmt.Errorf("eventlog: appending event: %w", err)
	}

	e.ID = ref.ID
	return e, nil
}

// Load streams every event in the log, ordered by recorded_at and then by
// document ID. It implements EventStore.
//
// The second ordering is what makes the order total: Firestore's
// timestamps are microsecond-resolution, so two events appended in the
// same instant would otherwise come back in an order the database was
// free to change between loads. Ordering a field and then __name__ is
// served by the field's single-field index, so this query needs no
// composite index — nothing to declare in Terraform, and nothing to
// forget to declare.
func (s *Store) Load(ctx context.Context) iter.Seq2[domain.Event, error] {
	return func(yield func(domain.Event, error) bool) {
		docs := s.events.
			OrderBy(recordedAtField, firestore.Asc).
			OrderBy(firestore.DocumentID, firestore.Asc).
			Documents(ctx)
		// Releases the stream both when the log runs out and when the
		// consumer breaks out of the range early.
		defer docs.Stop()

		for {
			snap, err := docs.Next()
			if errors.Is(err, iterator.Done) {
				return
			}
			if err != nil {
				// A fold that was cancelled is the caller's own doing; a
				// fold that failed is the database's. Consumers have to be
				// able to tell those apart — one is a request that went
				// away, the other is an outage — and the gRPC status the
				// iterator returns here says "Canceled" without unwrapping
				// to context.Canceled, so ask the context directly.
				if ctxErr := ctx.Err(); ctxErr != nil {
					yield(domain.Event{}, fmt.Errorf("eventlog: loading events: %w", ctxErr))
					return
				}
				yield(domain.Event{}, fmt.Errorf("eventlog: loading events: %w", err))
				return
			}

			e, err := decodeEvent(snap)
			if err != nil {
				yield(domain.Event{}, err)
				return
			}
			if !yield(e, nil) {
				return
			}
		}
	}
}

// eventDoc is the persisted shape of a domain.Event: the schema of a
// document in the events collection.
//
// It is spelled out separately from the domain type on purpose. These
// field names are written into an immutable log — every document already
// in the database is stuck with them — so they must not move when a Go
// field is renamed. It also keeps money.Cents out of the persistence
// contract: what is stored is a plain int64 of cents, which is what
// makes the amount readable by anything that opens the database, not
// just by this binary.
//
// The ID is not a field: it is the document's own ID.
type eventDoc struct {
	Action      string    `firestore:"action"`
	Month       string    `firestore:"month"`
	Type        string    `firestore:"type"`
	AmountCents int64     `firestore:"amount_cents"`
	Direction   string    `firestore:"direction"`
	Note        string    `firestore:"note"`
	RefEventID  string    `firestore:"ref_event_id"`
	RecordedAt  time.Time `firestore:"recorded_at"`
}

// recordedAtField is the primary sort key of the log, named as Firestore
// sees it. It is the one field name a query has to know, and it has to
// agree with the struct tag above.
const recordedAtField = "recorded_at"

func newEventDoc(e domain.Event) eventDoc {
	return eventDoc{
		Action:      string(e.Action),
		Month:       e.Month,
		Type:        e.Type,
		AmountCents: int64(e.Amount),
		Direction:   string(e.Direction),
		Note:        e.Note,
		RefEventID:  e.RefEventID,
		RecordedAt:  e.RecordedAt,
	}
}

// decodeEvent turns a document back into an event, and refuses to return
// one it cannot vouch for.
//
// Validating on the way out looks redundant — Append validated on the way
// in — but the two guard different things. Append guards against this
// build writing nonsense; this guards against reading nonsense that
// something else wrote: an older schema, a hand-edited document, a
// half-finished import. A fold is a sum, and an event that decodes to a
// zero amount or an unknown action is a total that is quietly wrong
// rather than loudly broken. The document ID travels with the error,
// because the only way to deal with a bad document in an append-only log
// is to go and look at it.
//
// It has one blind spot, and it is worth naming: a document with no
// recorded_at field at all never gets here. Firestore's ordered query
// returns only documents that have the field it orders by, so such a
// document is invisible to Load rather than rejected by it — it would be
// left silently out of every fold. Nothing this package writes can
// produce one; keeping anything else from writing one is a job for the
// Firestore security rules, in the deploy story.
func decodeEvent(snap *firestore.DocumentSnapshot) (domain.Event, error) {
	var doc eventDoc
	if err := snap.DataTo(&doc); err != nil {
		return domain.Event{}, fmt.Errorf("eventlog: decoding event %s: %w", snap.Ref.ID, err)
	}

	e := domain.Event{
		ID:         snap.Ref.ID,
		Action:     domain.Action(doc.Action),
		Month:      doc.Month,
		Type:       doc.Type,
		Amount:     money.Cents(doc.AmountCents),
		Direction:  domain.Direction(doc.Direction),
		Note:       doc.Note,
		RefEventID: doc.RefEventID,
		RecordedAt: doc.RecordedAt,
	}.Normalize()

	if err := e.Validate(); err != nil {
		return domain.Event{}, fmt.Errorf("eventlog: event %s: %w", snap.Ref.ID, err)
	}
	return e, nil
}

// Check proves the store can reach Firestore in both directions: it
// appends to the meta/health document and reads back what it wrote. A
// write alone would not catch a database that accepts writes but cannot
// serve reads, and a read alone would pass against an empty database the
// app has no permission to write to.
//
// It returns nil when the round-trip succeeded, and the reason otherwise.
// Callers should bound it with a context deadline: an unreachable
// Firestore fails by hanging, not by refusing.
func (s *Store) Check(ctx context.Context) error {
	// Increment rather than overwrite: the count is a cheap liveness
	// history, and it means the write is a real mutation the emulator and
	// the live service both have to accept.
	//
	// Truncated because Firestore stores timestamps to microsecond
	// precision: a nanosecond-precise time would come back rounded down
	// and read as older than the write that produced it.
	written := time.Now().UTC().Truncate(time.Microsecond)
	_, err := s.health.Set(ctx, map[string]any{
		"checked_at": written,
		"checks":     firestore.Increment(1),
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("write %s/%s: %w", MetaCollection, HealthDoc, err)
	}

	snap, err := s.health.Get(ctx)
	if err != nil {
		return fmt.Errorf("read %s/%s: %w", MetaCollection, HealthDoc, err)
	}
	if !snap.Exists() {
		return fmt.Errorf("read %s/%s: document missing immediately after write", MetaCollection, HealthDoc)
	}

	// The document is shared by every concurrent probe, so the timestamp
	// read back may belong to another check — do not require it to equal
	// what this call wrote. Requiring it to be *no older* than this
	// call's write still proves the read saw the write.
	var got struct {
		CheckedAt time.Time `firestore:"checked_at"`
	}
	if err := snap.DataTo(&got); err != nil {
		return fmt.Errorf("decode %s/%s: %w", MetaCollection, HealthDoc, err)
	}
	if got.CheckedAt.Before(written) {
		return fmt.Errorf("read %s/%s: stale read, checked_at %s predates the write at %s",
			MetaCollection, HealthDoc, got.CheckedAt, written)
	}

	return nil
}

// Close releases the Firestore client, and with it the connection.
//
// Even the emulator connection dialed above is the client's to close:
// option.WithGRPCConn hands it to a connection pool that closes it, so
// closing it here as well would be a double close — which gRPC reports as
// an error rather than ignoring.
func (s *Store) Close() error {
	return s.client.Close()
}

// emulatorCreds is the credential the Firestore emulator expects: it
// authenticates nobody, it just has to be present. It mirrors what the
// client library sends when it detects the emulator itself.
type emulatorCreds struct{}

func (emulatorCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer owner"}, nil
}

func (emulatorCreds) RequireTransportSecurity() bool { return false }
