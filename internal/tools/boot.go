package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/sysd"
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
	Default string       `json:"default_target"`
	Units   []chainEntry `json:"units"`
}

func registerBoot(s *mcp.Server, _ Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "boot_time",
		Description: "Per-phase boot timings from the systemd manager: firmware/loader " +
			"(when known), kernel, initrd (when used), userspace, and the total. " +
			"Pulled from org.freedesktop.systemd1 Manager properties over the system " +
			"D-Bus, so it works inside strict snap confinement.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ bootTimeIn) (*mcp.CallToolResult, bootTimeOut, error) {
		m, err := sysd.Open()
		if err != nil {
			return nil, bootTimeOut{}, fmt.Errorf("open systemd bus: %w", err)
		}
		defer m.Close()
		bt, err := m.BootTimes()
		if err != nil {
			return nil, bootTimeOut{}, fmt.Errorf("read boot times: %w", err)
		}
		out := composeBootTime(bt)
		if name, err := m.DefaultTarget(); err == nil && name != "" {
			out.TargetReached = name
			if path, err := m.GetUnit(name); err == nil {
				if t, err := m.UnitTimings(path, name); err == nil && t.ActiveEnterMonotonicUsec > 0 && bt.UserspaceMonotonicUsec > 0 {
					out.TargetReachedSec = float64(t.ActiveEnterMonotonicUsec-bt.UserspaceMonotonicUsec) / 1e6
				}
			}
		}
		return textResult("boot total %.2fs (kernel %.2fs · userspace %.2fs)",
			out.TotalSec, out.KernelSec, out.UserspaceSec), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "boot_blame",
		Description: "Per-unit initialization time during the current boot, sorted descending. " +
			"Computed from each unit's InactiveExit→ActiveEnter monotonic timestamp delta " +
			"via D-Bus. `top` caps the response (default 50, max 500).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in bootBlameIn) (*mcp.CallToolResult, bootBlameOut, error) {
		top := in.Top
		if top <= 0 {
			top = 50
		}
		if top > 500 {
			return nil, bootBlameOut{}, fmt.Errorf("top must be <= 500, got %d", top)
		}
		m, err := sysd.Open()
		if err != nil {
			return nil, bootBlameOut{}, fmt.Errorf("open systemd bus: %w", err)
		}
		defer m.Close()
		units, err := m.ListUnits()
		if err != nil {
			return nil, bootBlameOut{}, err
		}
		entries := make([]blameEntry, 0, len(units))
		for _, u := range units {
			t, err := m.UnitTimings(u.Path, u.Name)
			if err != nil {
				continue
			}
			dur := t.StartupUsec()
			if dur == 0 {
				continue
			}
			entries = append(entries, blameEntry{Unit: u.Name, InitSeconds: float64(dur) / 1e6})
		}
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].InitSeconds > entries[j].InitSeconds
		})
		if len(entries) > top {
			entries = entries[:top]
		}
		return textResult("blame: %d unit(s) returned", len(entries)),
			bootBlameOut{Count: len(entries), Entries: entries}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "boot_critical_chain",
		Description: "The chain of units that gated boot, walking After= dependencies " +
			"back from the default target, at each level picking the predecessor with " +
			"the latest ActiveEnter timestamp (i.e. the bottleneck on that hop).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ bootCriticalChainIn) (*mcp.CallToolResult, bootCriticalChainOut, error) {
		m, err := sysd.Open()
		if err != nil {
			return nil, bootCriticalChainOut{}, fmt.Errorf("open systemd bus: %w", err)
		}
		defer m.Close()
		target, err := m.DefaultTarget()
		if err != nil {
			return nil, bootCriticalChainOut{}, err
		}
		chain, err := walkCriticalChain(m, target)
		if err != nil {
			return nil, bootCriticalChainOut{}, err
		}
		return textResult("critical-chain: %d unit(s) (default %s)", len(chain), target),
			bootCriticalChainOut{Default: target, Units: chain}, nil
	})
}

// composeBootTime converts raw monotonic-microsecond timestamps into the
// per-phase seconds that systemd-analyze prints. Kept pure so it can be
// unit-tested without a live bus.
func composeBootTime(bt sysd.BootTimes) bootTimeOut {
	out := bootTimeOut{}
	// FirmwareTimestampMonotonic and LoaderTimestampMonotonic are stored as
	// the number of microseconds the phase ended BEFORE kernel boot (uint64,
	// 0 = phase didn't happen / not recorded). Firmware ran before the
	// loader, so firmware time is the part not covered by the loader.
	if bt.FirmwareMonotonicUsec > bt.LoaderMonotonicUsec {
		out.FirmwareSec = float64(bt.FirmwareMonotonicUsec-bt.LoaderMonotonicUsec) / 1e6
	}
	if bt.LoaderMonotonicUsec > 0 {
		out.LoaderSec = float64(bt.LoaderMonotonicUsec) / 1e6
	}
	// Kernel = time from kernel boot to either initrd start (when present)
	// or userspace start (when no initrd was used).
	if bt.InitRDMonotonicUsec > 0 {
		out.KernelSec = float64(bt.InitRDMonotonicUsec) / 1e6
		if bt.UserspaceMonotonicUsec > bt.InitRDMonotonicUsec {
			out.InitrdSec = float64(bt.UserspaceMonotonicUsec-bt.InitRDMonotonicUsec) / 1e6
		}
	} else if bt.UserspaceMonotonicUsec > 0 {
		out.KernelSec = float64(bt.UserspaceMonotonicUsec) / 1e6
	}
	if bt.FinishMonotonicUsec > bt.UserspaceMonotonicUsec {
		out.UserspaceSec = float64(bt.FinishMonotonicUsec-bt.UserspaceMonotonicUsec) / 1e6
	}
	out.TotalSec = out.FirmwareSec + out.LoaderSec + out.KernelSec + out.InitrdSec + out.UserspaceSec
	return out
}

// walkCriticalChain starts at the default target and at each step picks the
// predecessor (by After=) that became active latest. Stops at a unit with no
// After= deps, when we revisit a unit, or after a hard depth limit.
func walkCriticalChain(m *sysd.Manager, start string) ([]chainEntry, error) {
	const maxDepth = 32
	visited := map[string]struct{}{}
	var out []chainEntry
	cur := start
	for i := 0; i < maxDepth; i++ {
		if _, seen := visited[cur]; seen {
			break
		}
		visited[cur] = struct{}{}
		path, err := m.GetUnit(cur)
		if err != nil {
			return out, err
		}
		t, err := m.UnitTimings(path, cur)
		if err != nil {
			return out, err
		}
		out = append(out, chainEntry{
			Unit:            cur,
			ActiveAtSeconds: float64(t.ActiveEnterMonotonicUsec) / 1e6,
			StartupSeconds:  float64(t.StartupUsec()) / 1e6,
		})
		after, err := m.UnitAfter(path)
		if err != nil || len(after) == 0 {
			break
		}
		// Pick the predecessor with the latest ActiveEnter — that's the unit
		// systemd was waiting on at this hop.
		var nextName string
		var nextWhen uint64
		for _, name := range after {
			p, err := m.GetUnit(name)
			if err != nil {
				continue
			}
			pt, err := m.UnitTimings(p, name)
			if err != nil {
				continue
			}
			if pt.ActiveEnterMonotonicUsec > nextWhen {
				nextWhen = pt.ActiveEnterMonotonicUsec
				nextName = name
			}
		}
		if nextName == "" {
			break
		}
		cur = nextName
	}
	return out, nil
}
