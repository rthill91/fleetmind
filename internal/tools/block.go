package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/procfs"
	"github.com/gjolly/fleetmind/internal/snapconf"
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
		// Inside a strictly-confined snap, lsblk uses libmount which reads
		// /proc/self/mountinfo — the snap's mount namespace. That makes the
		// host's rootfs appear mounted at /var/lib/snapd/hostfs, shadows real
		// /boot with the snap base squashfs, and adds a long tail of
		// /etc/*-bind-mount entries. Rewrite mountpoint/mountpoints/fsroots
		// from PID 1's view (the host's mount namespace) so callers see the
		// host's actual mount table.
		if snapconf.InSnap() {
			if idx, err := buildHostMountIndex(d.ProcFS); err == nil {
				rewriteHostMounts(parsed, idx)
			}
			// Soft-fail: keep the raw lsblk output if /proc/1/mountinfo
			// is unreadable for any reason.
		}
		if nameRE != nil || len(in.Fields) > 0 {
			parsed = filterBlockDevices(parsed, nameRE, in.Fields)
		}
		return textResult("lsblk: %d bytes of JSON", len(stdout)), listBlockOut{Devices: parsed}, nil
	})
}

// buildHostMountIndex maps "major:minor" → all mount entries for that device
// in PID 1's mount namespace (the host's view). The kernel allocates
// major:minor namespace-independently, so values agree between the snap's
// mount NS and the host's, making this a safe key.
func buildHostMountIndex(pf procfs.Root) (map[string][]procfs.MountEntry, error) {
	entries, err := pf.MountsForPID("1")
	if err != nil {
		return nil, err
	}
	idx := make(map[string][]procfs.MountEntry, len(entries))
	for _, e := range entries {
		key := strconv.Itoa(e.DevMajor) + ":" + strconv.Itoa(e.DevMinor)
		idx[key] = append(idx[key], e)
	}
	return idx, nil
}

// rewriteHostMounts walks the parsed lsblk JSON and overwrites each device's
// mountpoint/mountpoints/fsroots with the host-namespace view from idx,
// recursing into "children".
func rewriteHostMounts(parsed map[string]any, idx map[string][]procfs.MountEntry) {
	devs, ok := parsed["blockdevices"].([]any)
	if !ok {
		return
	}
	for _, raw := range devs {
		if m, ok := raw.(map[string]any); ok {
			rewriteDeviceMounts(m, idx)
		}
	}
}

func rewriteDeviceMounts(m map[string]any, idx map[string][]procfs.MountEntry) {
	if mm, ok := m["maj:min"].(string); ok {
		if entries := idx[mm]; len(entries) > 0 {
			mps := make([]any, len(entries))
			roots := make([]any, len(entries))
			for i, e := range entries {
				mps[i] = e.MountPoint
				roots[i] = e.Root
			}
			m["mountpoint"] = entries[0].MountPoint
			m["mountpoints"] = mps
			m["fsroots"] = roots
		} else if _, present := m["maj:min"]; present {
			// Device exists but isn't mounted in the host NS — report as
			// unmounted regardless of what the snap NS shows.
			m["mountpoint"] = nil
			m["mountpoints"] = []any{nil}
			m["fsroots"] = []any{nil}
		}
	}
	if kids, ok := m["children"].([]any); ok {
		for _, kid := range kids {
			if km, ok := kid.(map[string]any); ok {
				rewriteDeviceMounts(km, idx)
			}
		}
	}
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
