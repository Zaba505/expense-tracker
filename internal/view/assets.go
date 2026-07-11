package view

import (
	"net/http"

	expensetracker "github.com/Zaba505/expense-tracker"
)

// AssetPrefix is the URL path the embedded assets are mounted under. It
// is the single source of truth: templates build their hrefs from it via
// AssetPath, and the router mounts AssetHandler on it.
const AssetPrefix = "/static/"

// AssetPath returns the URL for an embedded asset, named as it is in the
// static/ directory (e.g. "app.css").
func AssetPath(name string) string {
	return AssetPrefix + name
}

// AssetHandler serves the embedded assets. Mount it on AssetPrefix:
//
//	mux.Handle("GET "+view.AssetPrefix, view.AssetHandler())
func AssetHandler() http.Handler {
	return http.StripPrefix(AssetPrefix, http.FileServerFS(expensetracker.Static))
}
