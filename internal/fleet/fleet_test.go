package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	return NewRegistry(Peer{
		NodeID:        "self",
		AdvertiseURL:  "http://127.0.0.1:9000",
		Version:       "test",
		Tools:         []string{"a", "b"},
		JoinedAt:      time.Unix(1700000000, 0).UTC(),
		LastHeartbeat: time.Unix(1700000000, 0).UTC(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRegistryUpsertIgnoresSelfAndEmpty(t *testing.T) {
	r := newTestRegistry(t)
	if _, changed := r.Upsert(Peer{}); changed {
		t.Fatal("empty NodeID should be ignored")
	}
	if _, changed := r.Upsert(Peer{NodeID: "self"}); changed {
		t.Fatal("self NodeID should be ignored")
	}
	if got := len(r.Peers()); got != 0 {
		t.Fatalf("expected 0 remote peers, got %d", got)
	}
}

func TestRegistryUpsertAddAndRefresh(t *testing.T) {
	r := newTestRegistry(t)
	now := time.Unix(1700000100, 0).UTC()
	stored, changed := r.Upsert(Peer{NodeID: "n2", AdvertiseURL: "http://h2", LastHeartbeat: now})
	if !changed {
		t.Fatal("first upsert should be a change")
	}
	if stored.JoinedAt.IsZero() {
		t.Fatal("JoinedAt should default to now on insert")
	}
	// Same heartbeat → no change.
	if _, changed := r.Upsert(Peer{NodeID: "n2", AdvertiseURL: "http://h2", LastHeartbeat: now}); changed {
		t.Fatal("identical refresh should not report a change")
	}
	// Newer heartbeat → change.
	later := now.Add(5 * time.Second)
	if _, changed := r.Upsert(Peer{NodeID: "n2", LastHeartbeat: later}); !changed {
		t.Fatal("heartbeat advance should report a change")
	}
}

func TestRegistryPruneStale(t *testing.T) {
	r := newTestRegistry(t)
	old := time.Unix(1700000000, 0).UTC()
	now := old.Add(time.Minute)
	r.Upsert(Peer{NodeID: "stale", AdvertiseURL: "http://h", LastHeartbeat: old})
	r.Upsert(Peer{NodeID: "fresh", AdvertiseURL: "http://h2", LastHeartbeat: now})

	evicted := r.PruneStale(now, 30*time.Second)
	if len(evicted) != 1 || evicted[0].NodeID != "stale" {
		t.Fatalf("expected 'stale' to be evicted, got %+v", evicted)
	}
	if r.Has("stale") {
		t.Fatal("stale peer should be removed")
	}
	if !r.Has("fresh") {
		t.Fatal("fresh peer should remain")
	}
}

func TestRegistryBroadcastFanOut(t *testing.T) {
	r := newTestRegistry(t)
	ch1, cancel1 := r.Subscribe(4)
	defer cancel1()
	ch2, cancel2 := r.Subscribe(4)
	defer cancel2()

	want := Event{Type: EventHeartbeat, Peer: Peer{NodeID: "n3"}}
	r.Broadcast(want)
	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != want.Type || got.Peer.NodeID != want.Peer.NodeID {
				t.Fatalf("sub %d: got %+v, want %+v", i, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub %d: timed out waiting for broadcast", i)
		}
	}
}

func TestJoinHandlerRoundTrip(t *testing.T) {
	r := newTestRegistry(t)
	srv := httptest.NewServer(Handler(r))
	defer srv.Close()

	body, _ := json.Marshal(JoinRequest{Peer: Peer{
		NodeID:       "remote",
		AdvertiseURL: "http://remote:8080",
		Version:      "v1",
		Tools:        []string{"cpu_info"},
	}})
	resp, err := http.Post(srv.URL+"/fleet/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var jr JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		t.Fatal(err)
	}
	if len(jr.Peers) != 2 {
		t.Fatalf("expected roster of 2 (self + remote), got %d", len(jr.Peers))
	}
	if !r.Has("remote") {
		t.Fatal("remote not stored after /fleet/join")
	}
}

func TestJoinHandlerRejectsMissingFields(t *testing.T) {
	r := newTestRegistry(t)
	srv := httptest.NewServer(Handler(r))
	defer srv.Close()

	body, _ := json.Marshal(JoinRequest{Peer: Peer{NodeID: "x"}}) // no AdvertiseURL
	resp, err := http.Post(srv.URL+"/fleet/join", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestEventsHandlerReplaysAndStreams(t *testing.T) {
	r := newTestRegistry(t)
	srv := httptest.NewServer(Handler(r))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/fleet/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Replay should deliver the local self peer first.
	got := readEvent(t, resp.Body)
	if got.Type != EventPeerAdded || got.Peer.NodeID != "self" {
		t.Fatalf("expected replay of self, got %+v", got)
	}

	// Broadcast a new event and confirm we receive it.
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.Broadcast(Event{Type: EventHeartbeat, Peer: Peer{NodeID: "self", LastHeartbeat: time.Now().UTC()}})
	}()
	got = readEvent(t, resp.Body)
	if got.Type != EventHeartbeat || got.Peer.NodeID != "self" {
		t.Fatalf("expected heartbeat for self, got %+v", got)
	}
}

func readEvent(t *testing.T, body io.Reader) Event {
	t.Helper()
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	var acc strings.Builder
	for time.Now().Before(deadline) {
		n, err := body.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			if frame, ok := extractFrame(acc.String()); ok {
				return frame
			}
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	t.Fatal("timed out waiting for SSE event")
	return Event{}
}

func extractFrame(s string) (Event, bool) {
	idx := strings.Index(s, "\n\n")
	if idx < 0 {
		return Event{}, false
	}
	frame := s[:idx]
	var evt Event
	for _, line := range strings.Split(frame, "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			evt.Type = EventType(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			_ = json.Unmarshal([]byte(data), &evt.Peer)
		}
	}
	return evt, true
}
