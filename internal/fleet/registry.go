package fleet

import (
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Registry is the thread-safe view a single fleetmind node has of its fleet.
// It stores the local "self" Peer plus every remote peer it currently believes
// is alive, and multiplexes change events to local SSE subscribers.
type Registry struct {
	mu    sync.RWMutex
	self  Peer
	peers map[string]Peer

	subMu       sync.Mutex
	subscribers map[int]chan Event
	nextSubID   int

	log *slog.Logger
}

// NewRegistry builds an empty registry seeded with the local identity.
func NewRegistry(self Peer, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{
		self:        self,
		peers:       map[string]Peer{},
		subscribers: map[int]chan Event{},
		log:         log,
	}
}

// Self returns the local identity. The returned value is a copy.
func (r *Registry) Self() Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.self
}

// TouchSelfHeartbeat updates the local node's LastHeartbeat to now.
func (r *Registry) TouchSelfHeartbeat(now time.Time) Peer {
	r.mu.Lock()
	r.self.LastHeartbeat = now
	out := r.self
	r.mu.Unlock()
	return out
}

// Roster returns every known peer (local + remote) sorted by NodeID.
func (r *Registry) Roster() []Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Peer, 0, len(r.peers)+1)
	out = append(out, r.self)
	for _, p := range r.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Peers returns only remote peers, sorted by NodeID.
func (r *Registry) Peers() []Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Has reports whether a remote peer with the given NodeID is known.
func (r *Registry) Has(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.peers[nodeID]
	return ok
}

// Upsert records or refreshes a remote peer. Returns the stored Peer and
// whether the registry was changed (added or modified). Heartbeats from the
// local node and entries with a blank NodeID are ignored.
func (r *Registry) Upsert(p Peer) (Peer, bool) {
	if p.NodeID == "" || p.NodeID == r.self.NodeID {
		return p, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, existed := r.peers[p.NodeID]
	if !existed {
		if p.JoinedAt.IsZero() {
			p.JoinedAt = time.Now().UTC()
		}
		r.peers[p.NodeID] = p
		return p, true
	}
	// Preserve the original JoinedAt; refresh advertise URL/version/tools/heartbeat.
	merged := prev
	if p.AdvertiseURL != "" {
		merged.AdvertiseURL = p.AdvertiseURL
	}
	if p.Version != "" {
		merged.Version = p.Version
	}
	if len(p.Tools) > 0 {
		merged.Tools = p.Tools
	}
	if !p.LastHeartbeat.IsZero() {
		merged.LastHeartbeat = p.LastHeartbeat
	}
	r.peers[p.NodeID] = merged
	return merged, !samePeer(prev, merged)
}

// Remove drops a peer by NodeID. Returns the removed peer and whether it
// existed.
func (r *Registry) Remove(nodeID string) (Peer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.peers[nodeID]
	if !ok {
		return Peer{}, false
	}
	delete(r.peers, nodeID)
	return p, true
}

// PruneStale removes every remote peer whose LastHeartbeat is older than
// deadAfter relative to now, returning the evicted peers.
func (r *Registry) PruneStale(now time.Time, deadAfter time.Duration) []Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	var evicted []Peer
	for id, p := range r.peers {
		if p.LastHeartbeat.IsZero() {
			continue
		}
		if now.Sub(p.LastHeartbeat) > deadAfter {
			evicted = append(evicted, p)
			delete(r.peers, id)
		}
	}
	return evicted
}

// Subscribe registers a buffered channel that receives every Broadcast. The
// returned cancel func unregisters the subscriber and closes the channel.
// Slow subscribers drop events rather than blocking publishers.
func (r *Registry) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 16
	}
	ch := make(chan Event, buffer)
	r.subMu.Lock()
	id := r.nextSubID
	r.nextSubID++
	r.subscribers[id] = ch
	r.subMu.Unlock()
	return ch, func() {
		r.subMu.Lock()
		if existing, ok := r.subscribers[id]; ok {
			delete(r.subscribers, id)
			close(existing)
		}
		r.subMu.Unlock()
	}
}

// Broadcast delivers e to every subscriber. Subscribers whose buffer is full
// are skipped (event dropped) and logged at debug level.
func (r *Registry) Broadcast(e Event) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	for id, ch := range r.subscribers {
		select {
		case ch <- e:
		default:
			r.log.Debug("fleet subscriber dropped event", "sub_id", id, "event", string(e.Type))
		}
	}
}

func samePeer(a, b Peer) bool {
	if a.NodeID != b.NodeID || a.AdvertiseURL != b.AdvertiseURL || a.Version != b.Version {
		return false
	}
	if !a.LastHeartbeat.Equal(b.LastHeartbeat) {
		return false
	}
	if len(a.Tools) != len(b.Tools) {
		return false
	}
	for i := range a.Tools {
		if a.Tools[i] != b.Tools[i] {
			return false
		}
	}
	return true
}
