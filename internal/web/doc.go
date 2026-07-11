// Package web wires the net/http ServeMux, handlers, and middleware that
// serve the HTMX application, and runs the HTTP server itself.
//
// The split is deliberate: NewHandler builds the routed, middleware-wrapped
// handler and Serve runs it on a listener until its context is cancelled.
// Neither reads the environment nor calls os.Exit, so cmd/server stays a
// few lines of wiring and both halves are testable.
//
// The owner-allowlist auth middleware arrives with `story(auth)`.
package web
