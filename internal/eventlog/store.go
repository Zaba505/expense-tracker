package eventlog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

// Events is the append-only event collection. It is the seam the event
// append and load operations are built on (`story(eventlog)`, the event
// type and store); nothing outside this package should reach past it into
// the Firestore client.
func (s *Store) Events() *firestore.CollectionRef {
	return s.events
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
