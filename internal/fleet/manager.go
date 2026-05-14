package fleet

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ManagerOptions configures a Manager. Zero values for the durations fall back
// to sensible defaults.
type ManagerOptions struct {
	// BootstrapURL is the http(s) URL of an existing fleet node. Empty means
	// "start a solo fleet" — the local node forms a one-member roster.
	BootstrapURL string
	// Token is the shared bearer secret used on all outbound fleet requests.
	Token string
	// HeartbeatInterval is the period between local heartbeats.
	HeartbeatInterval time.Duration
	// DeadAfter is the staleness threshold past which peers are evicted.
	DeadAfter time.Duration
	// ReconnectInitial is the first SSE retry backoff. Doubles each failure
	// up to ReconnectMax.
	ReconnectInitial time.Duration
	ReconnectMax     time.Duration
	// HTTPClient is reused for short-lived requests. nil → default.
	HTTPClient *http.Client
}

func (o *ManagerOptions) applyDefaults() {
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = 10 * time.Second
	}
	if o.DeadAfter <= 0 {
		o.DeadAfter = 30 * time.Second
	}
	if o.ReconnectInitial <= 0 {
		o.ReconnectInitial = 1 * time.Second
	}
	if o.ReconnectMax <= 0 {
		o.ReconnectMax = 30 * time.Second
	}
}

// Manager drives the local node's participation in a fleet: it joins, maintains
// SSE streams to every known peer, emits periodic heartbeats and prunes
// stragglers. One Manager per process.
type Manager struct {
	opts   ManagerOptions
	reg    *Registry
	client *Client
	log    *slog.Logger

	// localEvents is the subscription used by the local-event loop. It is
	// established at construction time — before any HTTP listener can accept a
	// /fleet/join — so we never miss the very first peer's arrival.
	localEvents   <-chan Event
	cancelLocalEv func()

	streamsMu sync.Mutex
	streams   map[string]context.CancelFunc // nodeID → cancel for that peer's stream
}

// NewManager constructs a Manager bound to reg and immediately subscribes to
// reg's broadcast channel so events fired between construction and Run are
// buffered, not dropped.
func NewManager(reg *Registry, opts ManagerOptions, log *slog.Logger) *Manager {
	opts.applyDefaults()
	if log == nil {
		log = slog.Default()
	}
	ch, cancel := reg.Subscribe(128)
	return &Manager{
		opts:          opts,
		reg:           reg,
		client:        NewClient(opts.Token, opts.HTTPClient),
		log:           log,
		localEvents:   ch,
		cancelLocalEv: cancel,
		streams:       map[string]context.CancelFunc{},
	}
}

// Run executes the fleet lifecycle: bootstrap-join (if configured), heartbeat
// loop, pruning loop, and per-peer SSE consumers. It returns only when ctx is
// cancelled.
func (m *Manager) Run(ctx context.Context) {
	if m.opts.BootstrapURL != "" {
		if err := m.bootstrap(ctx); err != nil {
			m.log.Warn("fleet bootstrap failed; continuing in solo mode and will retry",
				"bootstrap", m.opts.BootstrapURL, "err", err)
			go m.retryBootstrap(ctx)
		}
	} else {
		m.log.Info("fleet solo mode (no --join-url provided)", "node_id", m.reg.Self().NodeID)
	}

	wg := sync.WaitGroup{}
	wg.Add(3)
	go func() { defer wg.Done(); m.heartbeatLoop(ctx) }()
	go func() { defer wg.Done(); m.pruneLoop(ctx) }()
	go func() { defer wg.Done(); m.localEventLoop(ctx) }()
	wg.Wait()
}

// localEventLoop reacts to events the local registry broadcasts (e.g. a peer
// arriving via /fleet/join, or one learned over gossip from another peer's
// SSE stream) by ensuring an outbound SSE stream exists for every peer we
// believe is alive. Without this, a node that only accepts inbound joins
// would never read its peers' heartbeats and would prune them on a timer.
//
// The subscription itself is created in NewManager — the loop only consumes —
// so an inbound join cannot race the subscriber.
func (m *Manager) localEventLoop(ctx context.Context) {
	defer m.cancelLocalEv()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-m.localEvents:
			if !open {
				return
			}
			self := m.reg.Self()
			if ev.Peer.NodeID == "" || ev.Peer.NodeID == self.NodeID {
				continue
			}
			switch ev.Type {
			case EventPeerAdded, EventHeartbeat:
				m.ensureStream(ctx, ev.Peer)
			case EventPeerRemoved:
				m.cancelStream(ev.Peer.NodeID)
			}
		}
	}
}

func (m *Manager) bootstrap(ctx context.Context) error {
	self := m.reg.Self()
	m.log.Info("joining fleet via bootstrap", "bootstrap", m.opts.BootstrapURL, "node_id", self.NodeID)
	roster, err := m.client.Join(ctx, m.opts.BootstrapURL, self)
	if err != nil {
		return err
	}
	for _, p := range roster {
		if p.NodeID == self.NodeID {
			continue
		}
		stored, changed := m.reg.Upsert(p)
		if changed {
			m.reg.Broadcast(Event{Type: EventPeerAdded, Peer: stored})
		}
		m.ensureStream(ctx, stored)
	}
	// Announce ourselves to every peer in the roster other than the bootstrap
	// — the bootstrap already knows us — so they connect to us symmetrically.
	for _, p := range roster {
		if p.NodeID == self.NodeID || sameBaseURL(p.AdvertiseURL, m.opts.BootstrapURL) {
			continue
		}
		if _, err := m.client.Join(ctx, p.AdvertiseURL, self); err != nil {
			m.log.Warn("announce to peer failed", "peer", p.AdvertiseURL, "err", err)
		}
	}
	return nil
}

func (m *Manager) retryBootstrap(ctx context.Context) {
	delay := m.opts.ReconnectInitial
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if err := m.bootstrap(ctx); err == nil {
			return
		}
		delay *= 2
		if delay > m.opts.ReconnectMax {
			delay = m.opts.ReconnectMax
		}
	}
}

func (m *Manager) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(m.opts.HeartbeatInterval)
	defer ticker.Stop()
	// Emit one immediately so subscribers don't have to wait for the first tick.
	self := m.reg.TouchSelfHeartbeat(time.Now().UTC())
	m.reg.Broadcast(Event{Type: EventHeartbeat, Peer: self})
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			self := m.reg.TouchSelfHeartbeat(now.UTC())
			m.reg.Broadcast(Event{Type: EventHeartbeat, Peer: self})
		}
	}
}

func (m *Manager) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(m.opts.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, dead := range m.reg.PruneStale(now.UTC(), m.opts.DeadAfter) {
				m.log.Info("peer evicted", "node_id", dead.NodeID, "advertise", dead.AdvertiseURL,
					"last_heartbeat", dead.LastHeartbeat)
				m.cancelStream(dead.NodeID)
				m.reg.Broadcast(Event{Type: EventPeerRemoved, Peer: dead})
			}
		}
	}
}

func (m *Manager) ensureStream(parent context.Context, peer Peer) {
	m.streamsMu.Lock()
	defer m.streamsMu.Unlock()
	if _, exists := m.streams[peer.NodeID]; exists {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.streams[peer.NodeID] = cancel
	go func() {
		defer cancel()
		m.runStream(ctx, peer)
	}()
}

func (m *Manager) cancelStream(nodeID string) {
	m.streamsMu.Lock()
	cancel, ok := m.streams[nodeID]
	delete(m.streams, nodeID)
	m.streamsMu.Unlock()
	if ok {
		cancel()
	}
}

func (m *Manager) runStream(ctx context.Context, peer Peer) {
	delay := m.opts.ReconnectInitial
	for {
		if ctx.Err() != nil {
			return
		}
		err := m.client.StreamEvents(ctx, peer.AdvertiseURL, func(e Event) {
			m.handleRemoteEvent(ctx, e)
			delay = m.opts.ReconnectInitial // reset backoff after any successful event
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			m.log.Debug("fleet stream dropped; will retry",
				"peer", peer.AdvertiseURL, "err", err, "backoff", delay)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay *= 2
		if delay > m.opts.ReconnectMax {
			delay = m.opts.ReconnectMax
		}
	}
}

func (m *Manager) handleRemoteEvent(ctx context.Context, e Event) {
	self := m.reg.Self()
	if e.Peer.NodeID == self.NodeID {
		return
	}
	switch e.Type {
	case EventHeartbeat, EventPeerAdded:
		if e.Peer.LastHeartbeat.IsZero() && e.Type == EventHeartbeat {
			e.Peer.LastHeartbeat = time.Now().UTC()
		}
		stored, changed := m.reg.Upsert(e.Peer)
		if changed && e.Type == EventPeerAdded {
			m.reg.Broadcast(Event{Type: EventPeerAdded, Peer: stored})
		}
		m.ensureStream(ctx, stored)
	case EventPeerRemoved:
		if removed, ok := m.reg.Remove(e.Peer.NodeID); ok {
			m.cancelStream(removed.NodeID)
			m.reg.Broadcast(Event{Type: EventPeerRemoved, Peer: removed})
		}
	}
}

func sameBaseURL(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	au, err := normalizeBaseURL(a)
	if err != nil {
		return false
	}
	bu, err := normalizeBaseURL(b)
	if err != nil {
		return false
	}
	return au == bu
}
