package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listFleetIn struct{}

type fleetMember struct {
	NodeID          string    `json:"node_id"`
	AdvertiseURL    string    `json:"advertise_url"`
	Version         string    `json:"version"`
	Tools           []string  `json:"tools"`
	JoinedAt        time.Time `json:"joined_at"`
	LastHeartbeat   time.Time `json:"last_heartbeat"`
	HeartbeatAgeSec float64   `json:"heartbeat_age_seconds"`
	Self            bool      `json:"self"`
}

type listFleetOut struct {
	Enabled    bool          `json:"enabled"`
	SelfNodeID string        `json:"self_node_id,omitempty"`
	Count      int           `json:"count"`
	Members    []fleetMember `json:"members"`
}

func registerFleet(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_fleet",
		Description: "List every MCP server the local node currently sees in its fleet, " +
			"including the local node. Each entry reports its NodeID, advertise URL, " +
			"version, MCP tool catalogue, join time and last heartbeat age.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listFleetIn) (*mcp.CallToolResult, listFleetOut, error) {
		if d.Fleet == nil {
			return textResult("fleet mode disabled"), listFleetOut{Enabled: false, Members: []fleetMember{}}, nil
		}
		self := d.Fleet.Self()
		now := time.Now().UTC()
		roster := d.Fleet.Roster()
		members := make([]fleetMember, 0, len(roster))
		for _, p := range roster {
			age := 0.0
			if !p.LastHeartbeat.IsZero() {
				age = now.Sub(p.LastHeartbeat).Seconds()
			}
			members = append(members, fleetMember{
				NodeID:          p.NodeID,
				AdvertiseURL:    p.AdvertiseURL,
				Version:         p.Version,
				Tools:           p.Tools,
				JoinedAt:        p.JoinedAt,
				LastHeartbeat:   p.LastHeartbeat,
				HeartbeatAgeSec: age,
				Self:            p.NodeID == self.NodeID,
			})
		}
		out := listFleetOut{
			Enabled:    true,
			SelfNodeID: self.NodeID,
			Count:      len(members),
			Members:    members,
		}
		return textResult("%d member(s) in fleet (self %s)", len(members), self.NodeID), out, nil
	})
}
