package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listBlockIn struct{}

type listBlockOut struct {
	// Devices is the parsed lsblk JSON object. lsblk -J emits
	// {"blockdevices": [...]} so we model it as an object map; deeper structure
	// is left as free-form to avoid pinning to a specific util-linux version.
	Devices map[string]any `json:"devices"`
}

func registerBlock(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_block_devices",
		Description: "Block-device tree as reported by `lsblk -J -O` (disks, partitions, LVM, RAID, holders).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listBlockIn) (*mcp.CallToolResult, listBlockOut, error) {
		stdout, _, err := d.Exec.Run(ctx, "lsblk", "-J", "-O")
		if err != nil {
			return nil, listBlockOut{}, fmt.Errorf("lsblk: %w", err)
		}
		parsed := map[string]any{}
		if err := json.Unmarshal(stdout, &parsed); err != nil {
			return nil, listBlockOut{}, fmt.Errorf("parse lsblk output: %w", err)
		}
		return textResult("lsblk: %d bytes of JSON", len(stdout)), listBlockOut{Devices: parsed}, nil
	})
}
