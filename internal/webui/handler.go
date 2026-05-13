package webui

import (
	"net/http"
	"strings"
)

// Handler returns an http.Handler that serves the embedded SPA under /ui/.
// GET /ui (no trailing slash) is redirected to /ui/ so relative asset paths
// resolve correctly.
//
// The static assets are NOT bearer-token protected: the page itself must load
// in the browser so the operator can paste their token, which is then attached
// to every API call the SPA makes against /mcp and /fleet/*.
func Handler() http.Handler {
	fileServer := http.FileServer(http.FS(assets()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" {
			http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			return
		}
		// index.html must never be cached so a fleetmind upgrade ships the
		// new entry point immediately. app.js and style.css are content-hashed
		// per build via go:embed bytes, but we keep their caching modest to
		// avoid stale shells after a snap refresh.
		switch {
		case r.URL.Path == "/ui/" || strings.HasSuffix(r.URL.Path, "/index.html"):
			w.Header().Set("Cache-Control", "no-cache")
		default:
			w.Header().Set("Cache-Control", "max-age=300")
		}
		// http.FileServer expects the URL path to be relative to the FS root.
		// Strip the /ui/ prefix so /ui/app.js maps to app.js in the embed.
		http.StripPrefix("/ui/", fileServer).ServeHTTP(w, r)
	})
}
