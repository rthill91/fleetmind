package fleet

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Handler returns an http.Handler that multiplexes the fleet endpoints:
//
//	POST /fleet/join   announce a peer and receive the current roster
//	GET  /fleet/peers  read the current roster
//	GET  /fleet/events SSE stream of heartbeat / peer_added / peer_removed
//
// All routes assume the caller has already validated the bearer token.
func Handler(reg *Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/fleet/join", joinHandler(reg))
	mux.HandleFunc("/fleet/peers", peersHandler(reg))
	mux.HandleFunc("/fleet/events", eventsHandler(reg))
	return mux
}

func joinHandler(reg *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req JoinRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid join body: %v", err), http.StatusBadRequest)
			return
		}
		if req.Peer.NodeID == "" || req.Peer.AdvertiseURL == "" {
			http.Error(w, "peer.node_id and peer.advertise_url are required", http.StatusBadRequest)
			return
		}
		if req.Peer.JoinedAt.IsZero() {
			req.Peer.JoinedAt = time.Now().UTC()
		}
		if req.Peer.LastHeartbeat.IsZero() {
			req.Peer.LastHeartbeat = req.Peer.JoinedAt
		}
		stored, changed := reg.Upsert(req.Peer)
		if changed {
			reg.Broadcast(Event{Type: EventPeerAdded, Peer: stored})
		}
		writeJSON(w, http.StatusOK, JoinResponse{Peers: reg.Roster()})
	}
}

func peersHandler(reg *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, RosterResponse{Peers: reg.Roster()})
	}
}

func eventsHandler(reg *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache, no-transform")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		ch, cancel := reg.Subscribe(64)
		defer cancel()

		// Replay the current roster so a freshly-attached subscriber starts
		// with a consistent view instead of waiting for the next heartbeat.
		for _, p := range reg.Roster() {
			if err := writeSSE(w, Event{Type: EventPeerAdded, Peer: p}); err != nil {
				return
			}
		}
		flusher.Flush()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, open := <-ch:
				if !open {
					return
				}
				if err := writeSSE(w, ev); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeSSE(w http.ResponseWriter, e Event) error {
	payload, err := json.Marshal(e.Peer)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload); err != nil {
		return fmt.Errorf("write sse: %w", err)
	}
	return nil
}
