// Package fleet implements a static, full-mesh peer membership layer for
// FleetMind. A node bootstraps into an existing fleet by POSTing its identity
// to one peer's /fleet/join endpoint; the response carries the full roster, and
// every member subsequently exchanges heartbeats over Server-Sent Events.
//
// The fleet channel reuses the daemon's bearer token as a shared fleet secret.
package fleet

import "time"

// EventType enumerates the messages exchanged on the SSE channel.
type EventType string

// Event types emitted on /fleet/events.
const (
	EventHeartbeat   EventType = "heartbeat"
	EventPeerAdded   EventType = "peer_added"
	EventPeerRemoved EventType = "peer_removed"
)

// Peer is the public, self-reported identity of a fleet member.
type Peer struct {
	NodeID        string    `json:"node_id"`
	AdvertiseURL  string    `json:"advertise_url"`
	Version       string    `json:"version"`
	Tools         []string  `json:"tools"`
	JoinedAt      time.Time `json:"joined_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// Event is the unit of the SSE stream. Type drives interpretation; Peer is the
// subject of the event (the heartbeating node, the added node, etc.).
type Event struct {
	Type EventType `json:"type"`
	Peer Peer      `json:"peer"`
}

// JoinRequest is the body of POST /fleet/join.
type JoinRequest struct {
	Peer Peer `json:"peer"`
}

// JoinResponse carries the full roster the calling node should connect to.
type JoinResponse struct {
	Peers []Peer `json:"peers"`
}

// RosterResponse is the body of GET /fleet/peers.
type RosterResponse struct {
	Peers []Peer `json:"peers"`
}
