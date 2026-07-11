// Package expensetracker is the module root. It carries no logic; it
// exists so the front-end assets in static/ can be embedded, because a
// package may only embed files at or below its own directory —
// internal/view, where they are used, cannot reach ../../static.
//
// Assets are embedded rather than fetched from a CDN so a deployed
// container is self-contained: the binary is the whole front end.
package expensetracker

import (
	"embed"
	"io/fs"
)

// The whole directory is embedded, so dropping a new asset into static/
// is enough to ship it — nothing here needs editing. Keep static/ to
// assets the browser should be able to fetch: everything in it is served.
//
//go:embed static
var embedded embed.FS

// Static is the vendored front-end assets (CSS + htmx), rooted at the
// static/ directory: paths are "app.css", not "static/app.css".
var Static = mustSub(embedded, "static")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("expensetracker: embedding " + dir + ": " + err.Error())
	}
	return sub
}
