package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fleetQueryIn struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type fleetNodeResult struct {
	NodeID       string         `json:"node_id"`
	AdvertiseURL string         `json:"advertise_url"`
	Self         bool           `json:"self"`
	Text         string         `json:"text,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type fleetQueryOut struct {
	Tool    string            `json:"tool"`
	Results []fleetNodeResult `json:"results"`
}

func registerFleetQuery(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "fleet_query",
		Description: "Fan-out a tool call to every node in the fleet (including self) and return " +
			"per-node results. Requires fleet mode to be enabled. " +
			"'tool' is the name of any FleetMind tool. " +
			"Optional 'arguments' are passed verbatim to each node.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in fleetQueryIn) (*mcp.CallToolResult, fleetQueryOut, error) {
		out := fleetQueryOut{Tool: in.Tool}
		if d.Fleet == nil {
			return textResult("fleet mode disabled"), out, nil
		}
		if in.Tool == "" {
			return textResult("tool name is required"), out, nil
		}

		roster := d.Fleet.Roster()
		self := d.Fleet.Self()

		results := make([]fleetNodeResult, len(roster))
		var wg sync.WaitGroup

		for i, p := range roster {
			wg.Add(1)
			go func(idx int, advertiseURL, nodeID string) {
				defer wg.Done()
				nr := fleetNodeResult{
					NodeID:       nodeID,
					AdvertiseURL: advertiseURL,
					Self:         nodeID == self.NodeID,
				}
				res, err := peerCallTool(ctx, advertiseURL, d.FleetToken, in.Tool, in.Arguments)
				if err != nil {
					nr.Error = err.Error()
				} else if res.IsError {
					for _, c := range res.Content {
						if tc, ok := c.(*mcp.TextContent); ok {
							nr.Error = tc.Text
							break
						}
					}
				} else {
					var parts []string
					for _, c := range res.Content {
						if tc, ok := c.(*mcp.TextContent); ok {
							parts = append(parts, tc.Text)
						}
					}
					nr.Text = strings.Join(parts, "\n")
					if sc, ok := res.StructuredContent.(map[string]any); ok {
						nr.Result = sc
					}
				}
				results[idx] = nr
			}(i, p.AdvertiseURL, p.NodeID)
		}
		wg.Wait()

		out.Results = results
		var b strings.Builder
		fmt.Fprintf(&b, "fleet_query %q — %d node(s):\n", in.Tool, len(results))
		for _, r := range results {
			label := r.NodeID
			if r.Self {
				label += " (self)"
			}
			if r.Error != "" {
				fmt.Fprintf(&b, "  %-40s error: %s\n", label, r.Error)
			} else if r.Text != "" {
				fmt.Fprintf(&b, "  %-40s %s\n", label, r.Text)
			} else {
				fmt.Fprintf(&b, "  %-40s (no output)\n", label)
			}
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(b.String())}},
		}, out, nil
	})
}

// peerCallTool opens a short-lived MCP session to advertiseURL and calls the
// named tool, returning the raw result. A 30-second per-peer timeout is applied.
func peerCallTool(ctx context.Context, advertiseURL, token, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint: advertiseURL + "/mcp",
		HTTPClient: &http.Client{
			Transport: &fleetTokenTransport{base: http.DefaultTransport, token: token},
			Timeout:   30 * time.Second,
		},
		DisableStandaloneSSE: true,
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "fleetmind", Version: "internal"}, nil)
	session, err := c.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", advertiseURL, err)
	}
	defer session.Close()
	if args == nil {
		args = map[string]any{}
	}
	return session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
}

type fleetTokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *fleetTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}
