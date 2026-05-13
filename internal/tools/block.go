package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listBlockIn struct {
	NameRegex string   `json:"name_regex,omitempty" jsonschema:"keep only top-level devices whose name matches this regex (max 200 chars). Children are kept unfiltered."`
	Fields    []string `json:"fields,omitempty" jsonschema:"project each device entry to only these keys (max 32 entries). Empty = return every key."`
}

type listBlockOut struct {
	// Devices is the parsed lsblk JSON object. lsblk -J emits
	// {"blockdevices": [...]} so we model it as an object map; deeper structure
	// is left as free-form to avoid pinning to a specific util-linux version.
	Devices map[string]any `json:"devices"`
}

func registerBlock(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_block_devices",
		Description: "Block-device tree as reported by `lsblk -J -O` (disks, partitions, LVM, RAID, holders). Optional: name_regex filters top-level devices; fields projects each entry to a subset of keys.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listBlockIn) (*mcp.CallToolResult, listBlockOut, error) {
		var nameRE *regexp.Regexp
		if in.NameRegex != "" {
			if len(in.NameRegex) > 200 {
				return nil, listBlockOut{}, fmt.Errorf("name_regex too long (%d > 200)", len(in.NameRegex))
			}
			re, err := regexp.Compile(in.NameRegex)
			if err != nil {
				return nil, listBlockOut{}, fmt.Errorf("name_regex does not compile: %w", err)
			}
			nameRE = re
		}
		if len(in.Fields) > 32 {
			return nil, listBlockOut{}, fmt.Errorf("fields too long (%d > 32)", len(in.Fields))
		}
		stdout, _, err := d.Exec.Run(ctx, "lsblk", "-J", "-O")
		if err != nil {
			return nil, listBlockOut{}, fmt.Errorf("lsblk: %w", err)
		}
		parsed := map[string]any{}
		if err := json.Unmarshal(stdout, &parsed); err != nil {
			return nil, listBlockOut{}, fmt.Errorf("parse lsblk output: %w", err)
		}
		if nameRE != nil || len(in.Fields) > 0 {
			parsed = filterBlockDevices(parsed, nameRE, in.Fields)
		}
		return textResult("lsblk: %d bytes of JSON", len(stdout)), listBlockOut{Devices: parsed}, nil
	})
}

// filterBlockDevices applies name_regex (top level only) and fields projection
// (applied recursively to every device dict) to the parsed lsblk output.
func filterBlockDevices(in map[string]any, nameRE *regexp.Regexp, fields []string) map[string]any {
	out := map[string]any{}
	devs, ok := in["blockdevices"].([]any)
	if !ok {
		return in
	}
	var keep []any
	for _, raw := range devs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if nameRE != nil {
			name, _ := m["name"].(string)
			if !nameRE.MatchString(name) {
				continue
			}
		}
		keep = append(keep, projectDevice(m, fields))
	}
	out["blockdevices"] = keep
	return out
}

// projectDevice returns a copy of m with only the keys in fields (if any),
// recursing into the "children" array. "name" is always preserved so children
// remain identifiable even with a narrow projection.
func projectDevice(m map[string]any, fields []string) map[string]any {
	if len(fields) == 0 {
		// Still recurse so projection applied at higher level propagates.
		if kids, ok := m["children"].([]any); ok {
			out := make(map[string]any, len(m))
			for k, v := range m {
				out[k] = v
			}
			projected := make([]any, 0, len(kids))
			for _, kid := range kids {
				if km, ok := kid.(map[string]any); ok {
					projected = append(projected, projectDevice(km, fields))
				}
			}
			out["children"] = projected
			return out
		}
		return m
	}
	wanted := map[string]struct{}{"name": {}}
	for _, f := range fields {
		wanted[f] = struct{}{}
	}
	out := map[string]any{}
	for k, v := range m {
		if _, ok := wanted[k]; ok {
			out[k] = v
		}
	}
	if kids, ok := m["children"].([]any); ok {
		projected := make([]any, 0, len(kids))
		for _, kid := range kids {
			if km, ok := kid.(map[string]any); ok {
				projected = append(projected, projectDevice(km, fields))
			}
		}
		out["children"] = projected
	}
	return out
}
