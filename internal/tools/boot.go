package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// -------- boot_time ----------------------------------------------------------

type bootTimeIn struct{}

type bootTimeOut struct {
	FirmwareSec      float64 `json:"firmware_seconds,omitempty"`
	LoaderSec        float64 `json:"loader_seconds,omitempty"`
	KernelSec        float64 `json:"kernel_seconds,omitempty"`
	InitrdSec        float64 `json:"initrd_seconds,omitempty"`
	UserspaceSec     float64 `json:"userspace_seconds,omitempty"`
	TotalSec         float64 `json:"total_seconds"`
	TargetReached    string  `json:"target_reached,omitempty"`
	TargetReachedSec float64 `json:"target_reached_seconds,omitempty"`
	Raw              string  `json:"raw"`
}

// -------- boot_blame ---------------------------------------------------------

type bootBlameIn struct {
	Top int `json:"top,omitempty" jsonschema:"return only the top N slowest units (default 50, max 500)"`
}

type blameEntry struct {
	Unit        string  `json:"unit"`
	InitSeconds float64 `json:"init_seconds"`
}

type bootBlameOut struct {
	Count   int          `json:"count"`
	Entries []blameEntry `json:"entries"`
}

// -------- boot_critical_chain -----------------------------------------------

type bootCriticalChainIn struct{}

type chainEntry struct {
	Unit            string  `json:"unit"`
	ActiveAtSeconds float64 `json:"active_at_seconds,omitempty"`
	StartupSeconds  float64 `json:"startup_seconds,omitempty"`
}

type bootCriticalChainOut struct {
	Units []chainEntry `json:"units"`
	Raw   string       `json:"raw"`
}

func registerBoot(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "boot_time",
		Description: "Parsed `systemd-analyze time` output: per-phase boot timings " +
			"(firmware, loader, kernel, initrd, userspace) and the total. " +
			"Phases that don't apply on this host are reported as 0 (e.g. firmware on a VM).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ bootTimeIn) (*mcp.CallToolResult, bootTimeOut, error) {
		stdout, _, err := d.Exec.Run(ctx, "systemd-analyze", "time")
		if err != nil {
			return nil, bootTimeOut{}, fmt.Errorf("systemd-analyze time: %w", err)
		}
		out := parseBootTime(string(stdout))
		return textResult("boot total %.2fs (kernel %.2fs · userspace %.2fs)",
			out.TotalSec, out.KernelSec, out.UserspaceSec), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "boot_blame",
		Description: "Parsed `systemd-analyze blame` output: per-unit initialization time, " +
			"sorted descending. Use `top` to cap the response (default 50).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bootBlameIn) (*mcp.CallToolResult, bootBlameOut, error) {
		top := in.Top
		if top <= 0 {
			top = 50
		}
		if top > 500 {
			return nil, bootBlameOut{}, fmt.Errorf("top must be <= 500, got %d", top)
		}
		stdout, _, err := d.Exec.Run(ctx, "systemd-analyze", "blame")
		if err != nil {
			return nil, bootBlameOut{}, fmt.Errorf("systemd-analyze blame: %w", err)
		}
		entries := parseBlame(string(stdout))
		if len(entries) > top {
			entries = entries[:top]
		}
		return textResult("blame: %d unit(s) returned", len(entries)),
			bootBlameOut{Count: len(entries), Entries: entries}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "boot_critical_chain",
		Description: "Parsed `systemd-analyze critical-chain`: the serialized chain of units " +
			"that gated the default target (typically multi-user.target). Each entry carries " +
			"the cumulative active-at time and the unit's own startup time when systemd reports it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ bootCriticalChainIn) (*mcp.CallToolResult, bootCriticalChainOut, error) {
		stdout, _, err := d.Exec.Run(ctx, "systemd-analyze", "critical-chain", "--no-pager")
		if err != nil {
			return nil, bootCriticalChainOut{}, fmt.Errorf("systemd-analyze critical-chain: %w", err)
		}
		raw := string(stdout)
		units := parseCriticalChain(raw)
		return textResult("critical-chain: %d unit(s)", len(units)),
			bootCriticalChainOut{Units: units, Raw: raw}, nil
	})
}

// ---------- parsers (kept pure so they can be unit-tested) -------------------

// durRE matches one or more composite tokens (e.g. "5.219s", "1min 23.456s")
// without consuming trailing whitespace.
const durRE = `[0-9]+(?:\.[0-9]+)?(?:y|month|w|d|h|min|ms|s)(?:\s+[0-9]+(?:\.[0-9]+)?(?:y|month|w|d|h|min|ms|s))*`

var (
	// "5.219s (firmware)" / "234ms (loader)" / "1min 23.456s (userspace)".
	segmentRE = regexp.MustCompile(`(` + durRE + `)\s+\(([a-z]+)\)`)
	// "= 19.935s" (total, after the chain of segments).
	totalRE = regexp.MustCompile(`=\s+(` + durRE + `)`)
	// "multi-user.target reached after 12.123s in userspace."
	targetRE = regexp.MustCompile(`(\S+\.target)\s+reached after\s+(` + durRE + `)\s+in (?:userspace|initrd)`)
	// Units inside critical-chain tree lines: "<name> @<active> [+<startup>]".
	chainUnitRE = regexp.MustCompile(`([A-Za-z0-9@:_.\-]+\.(?:target|service|socket|timer|mount|swap|path|slice|scope|device))(?:\s+@(` + durRE + `))?(?:\s+\+(` + durRE + `))?`)
)

func parseBootTime(s string) bootTimeOut {
	out := bootTimeOut{Raw: s}
	for _, m := range segmentRE.FindAllStringSubmatch(s, -1) {
		secs, ok := parseDurationSecs(m[1])
		if !ok {
			continue
		}
		switch m[2] {
		case "firmware":
			out.FirmwareSec = secs
		case "loader":
			out.LoaderSec = secs
		case "kernel":
			out.KernelSec = secs
		case "initrd":
			out.InitrdSec = secs
		case "userspace":
			out.UserspaceSec = secs
		}
	}
	if m := totalRE.FindStringSubmatch(s); m != nil {
		if secs, ok := parseDurationSecs(m[1]); ok {
			out.TotalSec = secs
		}
	}
	if m := targetRE.FindStringSubmatch(s); m != nil {
		out.TargetReached = m[1]
		if secs, ok := parseDurationSecs(m[2]); ok {
			out.TargetReachedSec = secs
		}
	}
	return out
}

func parseBlame(s string) []blameEntry {
	var out []blameEntry
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		unit := fields[len(fields)-1]
		durStr := strings.Join(fields[:len(fields)-1], " ")
		secs, ok := parseDurationSecs(durStr)
		if !ok {
			continue
		}
		out = append(out, blameEntry{Unit: unit, InitSeconds: secs})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].InitSeconds > out[j].InitSeconds
	})
	return out
}

func parseCriticalChain(s string) []chainEntry {
	var out []chainEntry
	for _, line := range strings.Split(s, "\n") {
		m := chainUnitRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		e := chainEntry{Unit: m[1]}
		if m[2] != "" {
			if v, ok := parseDurationSecs(m[2]); ok {
				e.ActiveAtSeconds = v
			}
		}
		if m[3] != "" {
			if v, ok := parseDurationSecs(m[3]); ok {
				e.StartupSeconds = v
			}
		}
		out = append(out, e)
	}
	return out
}

// parseDurationSecs accepts the composite duration forms systemd-analyze emits:
// "234ms", "1.234s", "1min 23.456s", "2h 3min 4s". Returns total seconds.
// Unknown suffixes (e.g. "y", "month") are tolerated but contribute 0 — boot
// timings should never reach those scales, and if they do the user can read
// the Raw field directly.
func parseDurationSecs(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var total float64
	seen := false
	for _, f := range strings.Fields(s) {
		mult, num, ok := splitDuration(f)
		if !ok {
			return 0, false
		}
		total += num * mult
		seen = true
	}
	return total, seen
}

func splitDuration(f string) (mult, num float64, ok bool) {
	// Order matters: "ms" must be checked before "s", "min" before "n", etc.
	suffixes := []struct {
		s    string
		mult float64
	}{
		{"ms", 0.001},
		{"min", 60},
		{"month", 30 * 24 * 3600},
		{"s", 1},
		{"h", 3600},
		{"d", 24 * 3600},
		{"w", 7 * 24 * 3600},
		{"y", 365 * 24 * 3600},
	}
	for _, suf := range suffixes {
		if strings.HasSuffix(f, suf.s) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(f, suf.s), 64)
			if err != nil {
				return 0, 0, false
			}
			return suf.mult, v, true
		}
	}
	return 0, 0, false
}
