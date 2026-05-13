package mcpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	h := bearerAuth(inner, "s3cret", log)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic abcdef", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"correct", "Bearer s3cret", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d", rec.Code, c.want)
			}
			if c.want == http.StatusUnauthorized {
				if got := rec.Header().Get("WWW-Authenticate"); got == "" {
					t.Errorf("missing WWW-Authenticate on 401")
				}
			}
		})
	}
}
