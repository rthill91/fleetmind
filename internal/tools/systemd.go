package tools

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/sysd"
)

// -------- list_systemd_units ------------------------------------------------

type listUnitsIn struct {
	State string `json:"state,omitempty" jsonschema:"filter by active state: active|inactive|failed|activating|deactivating|reloading"`
	Type  string `json:"type,omitempty" jsonschema:"filter by unit type suffix: service|timer|socket|mount|target|path|swap|slice|scope|device|automount"`
	Limit int    `json:"limit,omitempty" jsonschema:"max number of units to return (default 500, max 2000)"`
}

type systemdUnit struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

type listUnitsOut struct {
	Count int           `json:"count"`
	Units []systemdUnit `json:"units"`
}

// -------- unit_status -------------------------------------------------------

type unitStatusIn struct {
	Unit         string `json:"unit" jsonschema:"unit name, e.g. NetworkManager.service"`
	JournalLines int    `json:"journal_lines,omitempty" jsonschema:"how many recent journal entries to include for this unit (default 30, max 500)"`
}

type unitStatusOut struct {
	Unit       string            `json:"unit"`
	Properties map[string]string `json:"properties"`
	Journal    string            `json:"journal,omitempty"`
}

// -------- list_timers -------------------------------------------------------

type listTimersIn struct{}

type systemdTimer struct {
	Unit                     string `json:"unit"`
	Activates                string `json:"activates"`
	NextElapseMonotonicUsec  uint64 `json:"next_elapse_monotonic_usec,omitempty"`
	NextElapseRealtimeUsec   uint64 `json:"next_elapse_realtime_usec,omitempty"`
	LastTriggerUsec          uint64 `json:"last_trigger_usec,omitempty"`
	LastTriggerMonotonicUsec uint64 `json:"last_trigger_monotonic_usec,omitempty"`
}

type listTimersOut struct {
	Count  int            `json:"count"`
	Timers []systemdTimer `json:"timers"`
}

// ---------------------------------------------------------------------------

// allowedUnitStates / allowedUnitTypes keep the filter inputs explicit; the
// D-Bus surface itself doesn't validate, so we do it here.
var (
	allowedUnitStates = map[string]bool{
		"active": true, "inactive": true, "failed": true,
		"activating": true, "deactivating": true, "reloading": true,
	}
	allowedUnitTypes = map[string]bool{
		"service": true, "timer": true, "socket": true, "mount": true,
		"target": true, "path": true, "swap": true, "slice": true,
		"scope": true, "device": true, "automount": true,
	}
)

func registerSystemd(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_systemd_units",
		Description: "All loaded systemd units (via Manager.ListUnits over D-Bus) with " +
			"optional state/type filters. Default cap is 500 entries — narrow with " +
			"`state` (e.g. \"failed\") or `type` (e.g. \"service\", \"timer\") for focused " +
			"triage.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listUnitsIn) (*mcp.CallToolResult, listUnitsOut, error) {
		if in.State != "" && !allowedUnitStates[in.State] {
			return nil, listUnitsOut{}, fmt.Errorf("invalid state %q", in.State)
		}
		if in.Type != "" && !allowedUnitTypes[in.Type] {
			return nil, listUnitsOut{}, fmt.Errorf("invalid type %q", in.Type)
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 500
		}
		if limit > 2000 {
			return nil, listUnitsOut{}, fmt.Errorf("limit must be <= 2000, got %d", limit)
		}
		m, err := sysd.Open()
		if err != nil {
			return nil, listUnitsOut{}, fmt.Errorf("open systemd bus: %w", err)
		}
		defer m.Close()
		raw, err := m.ListUnits()
		if err != nil {
			return nil, listUnitsOut{}, err
		}
		out := listUnitsOut{Units: make([]systemdUnit, 0, len(raw))}
		typeSuffix := ""
		if in.Type != "" {
			typeSuffix = "." + in.Type
		}
		for _, u := range raw {
			if in.State != "" && u.ActiveState != in.State {
				continue
			}
			if typeSuffix != "" && !strings.HasSuffix(u.Name, typeSuffix) {
				continue
			}
			out.Units = append(out.Units, systemdUnit{
				Unit: u.Name, Load: u.LoadState, Active: u.ActiveState,
				Sub: u.SubState, Description: u.Description,
			})
		}
		sort.SliceStable(out.Units, func(i, j int) bool { return out.Units[i].Unit < out.Units[j].Unit })
		if len(out.Units) > limit {
			out.Units = out.Units[:limit]
		}
		out.Count = len(out.Units)
		return textResult("%d unit(s) matched", out.Count), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "unit_status",
		Description: "Detail view of a single systemd unit: every property the unit " +
			"publishes on org.freedesktop.systemd1.Unit, plus a tail of the unit's recent " +
			"journal (best-effort; requires log-observe).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in unitStatusIn) (*mcp.CallToolResult, unitStatusOut, error) {
		if !unitRE.MatchString(in.Unit) {
			return nil, unitStatusOut{}, fmt.Errorf("invalid unit name %q", in.Unit)
		}
		jLines := in.JournalLines
		if jLines <= 0 {
			jLines = 30
		}
		if jLines > 500 {
			return nil, unitStatusOut{}, fmt.Errorf("journal_lines must be <= 500, got %d", jLines)
		}
		m, err := sysd.Open()
		if err != nil {
			return nil, unitStatusOut{}, fmt.Errorf("open systemd bus: %w", err)
		}
		defer m.Close()
		path, err := m.LoadUnit(in.Unit)
		if err != nil {
			return nil, unitStatusOut{}, err
		}
		props, err := m.UnitPropertiesAll(path, "org.freedesktop.systemd1.Unit")
		if err != nil {
			return nil, unitStatusOut{}, err
		}
		// Journal is still pulled via journalctl — log-observe makes journalctl
		// work; no D-Bus involved on that side.
		journal := ""
		if d.Exec != nil {
			jOut, _, jErr := d.Exec.Run(ctx, "journalctl", "--no-pager", "-o", "short-iso",
				"-n", strconv.Itoa(jLines), "--unit", in.Unit)
			if jErr == nil {
				journal = string(jOut)
			}
		}
		summary := props["ActiveState"]
		if sub := props["SubState"]; sub != "" {
			summary = summary + "/" + sub
		}
		return textResult("%s: %s", in.Unit, summary),
			unitStatusOut{Unit: in.Unit, Properties: props, Journal: journal}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_timers",
		Description: "All loaded systemd timer units with their scheduling timestamps " +
			"(next/last elapse, monotonic & realtime). Reads org.freedesktop.systemd1.Timer " +
			"properties over D-Bus.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listTimersIn) (*mcp.CallToolResult, listTimersOut, error) {
		m, err := sysd.Open()
		if err != nil {
			return nil, listTimersOut{}, fmt.Errorf("open systemd bus: %w", err)
		}
		defer m.Close()
		raw, err := m.ListUnits()
		if err != nil {
			return nil, listTimersOut{}, err
		}
		out := listTimersOut{Timers: make([]systemdTimer, 0)}
		for _, u := range raw {
			if !strings.HasSuffix(u.Name, ".timer") {
				continue
			}
			info, _ := m.TimerProperties(u.Path)
			activates, _ := m.TriggersUnit(u.Path)
			out.Timers = append(out.Timers, systemdTimer{
				Unit:                     u.Name,
				Activates:                activates,
				NextElapseMonotonicUsec:  info.NextElapseMonotonicUsec,
				NextElapseRealtimeUsec:   info.NextElapseRealtimeUsec,
				LastTriggerUsec:          info.LastTriggerUsec,
				LastTriggerMonotonicUsec: info.LastTriggerMonotonicUsec,
			})
		}
		sort.SliceStable(out.Timers, func(i, j int) bool { return out.Timers[i].Unit < out.Timers[j].Unit })
		out.Count = len(out.Timers)
		return textResult("%d timer(s)", out.Count), out, nil
	})
}
