package mcpserver

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
)

// bearerAuth wraps next with a constant-time bearer-token check. The realm is
// fixed to "fleetmind" for WWW-Authenticate challenges.
func bearerAuth(next http.Handler, token string, log *slog.Logger) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fleetmind"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			log.Debug("auth rejected", "remote", r.RemoteAddr, "path", r.URL.Path)
			return
		}
		next.ServeHTTP(w, r)
	})
}
