package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesIndex(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/ui/")
	if err != nil {
		t.Fatalf("get /ui/: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/ status = %d, want 200", res.StatusCode)
	}
	body := readBody(t, res)
	if !strings.Contains(body, "<title>FleetMind</title>") {
		t.Fatalf("index.html missing expected title, got: %q", truncate(body, 120))
	}
}

func TestHandlerServesAssets(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/ui/app.js", "/ui/style.css"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, res.StatusCode)
		}
	}
}

func TestHandlerRedirectsTrailingSlash(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	// Default client follows redirects; disable so we can inspect the 301.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Get(srv.URL + "/ui")
	if err != nil {
		t.Fatalf("get /ui: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("GET /ui status = %d, want 301", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/ui/" {
		t.Fatalf("redirect Location = %q, want %q", got, "/ui/")
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	buf := make([]byte, 4096)
	var out strings.Builder
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return out.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
