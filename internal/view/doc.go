// Package view holds the templ-generated HTML components and layouts
// rendered as full pages and, later, as HTMX partials. It also owns the
// URL space of the front-end assets — the paths its templates reference
// and the handler that serves them are defined together here, so they
// cannot drift apart.
//
// The asset bytes themselves are embedded at the module root (see
// expensetracker.Static): go:embed cannot reach out of its own package's
// directory into static/.
package view
