// Package auth signs the owner in with Google and remembers that they are
// signed in.
//
// The flow is ordinary OIDC authorization code: GET /auth/login mints a
// state and a nonce and sends the browser to Google; GET /auth/callback
// takes the code back, trades it for an ID token, verifies that token, and
// puts a session cookie on the browser. What each of those two values
// defends against, and why the check cannot be skipped, is argued at the
// call site.
//
// # No session store
//
// A session here is a signed cookie and nothing else — there is no table,
// no Firestore collection, no memory map. That is a deliberate fit to the
// deployment: the app scales to zero and runs on up to a handful of
// interchangeable Cloud Run instances, so a session kept in an instance's
// memory would be lost on the next cold start (which, at min-instances 0,
// is most requests) and unknown to the other instance. A signed cookie is
// verifiable by any instance, including one that has not started yet.
//
// The cost is that a session cannot be revoked server-side: the cookie is
// good until it expires. That is why SessionTTL is a day rather than a
// month, and it is the whole reason SESSION_KEY is a secret — anyone
// holding it can mint a session for any email.
//
// # Who may sign in
//
// This package authenticates; it does not authorize. It will hand a
// session to any Google account with a verified email, because knowing who
// somebody is and deciding whether they may use the app are separate
// questions. internal/web is the package that turns a session into access
// by comparing its email with OWNER_EMAIL.
package auth
