// Package webui ships a static single-page operator console embedded in the
// fleetmind binary. The assets are served at /ui/ and call the existing
// /healthz, /fleet/* and /mcp endpoints with the operator-supplied bearer
// token; no UI-specific server state is added.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticFS embed.FS

// assets returns the embedded filesystem rooted at the static directory so
// http.FileServer serves /ui/index.html, /ui/app.js, etc. without a "static/"
// prefix.
func assets() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Unreachable: the embed directive above guarantees the directory
		// exists at build time.
		panic("webui: static directory missing from embed: " + err.Error())
	}
	return sub
}
