package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// -------- list_systemd_units ------------------------------------------------

type listUnitsIn struct {
	State string `json:"state,omitempty" jsonschema:"filter by active state: active|inactive|failed|activating|deactivating|reloading"`
	Type  string `json:"type,omitempty" jsonschema:"filter by unit type: service|timer|socket|mount|target|path|swap|slice|scope|device"`
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

// systemdTimer mirrors `systemctl list-timers --output=json` entries. The
// next/left/last/passed fields are emitted as numbers (microseconds since the
// Unix epoch; 0 means "never"), not strings — verified against systemd v255.
type systemdTimer struct {
	NextMicros   int64  `json:"next_micros"`
	LeftMicros   int64  `json:"left_micros"`
	LastMicros   int64  `json:"last_micros"`
	PassedMicros int64  `json:"passed_micros"`
	Unit         string `json:"unit"`
	Activates    string `json:"activates"`
}

// rawSystemdTimer is the on-the-wire shape. Mapped into systemdTimer before
// returning so the public field names are explicit about the microsecond unit.
type rawSystemdTimer struct {
	Next      int64  `json:"next"`
	Left      int64  `json:"left"`
	Last      int64  `json:"last"`
	Passed    int64  `json:"passed"`
	Unit      string `json:"unit"`
	Activates string `json:"activates"`
}

type listTimersOut struct {
	Count  int            `json:"count"`
	Timers []systemdTimer `json:"timers"`
}

// ---------------------------------------------------------------------------

func registerSystemd(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_systemd_units",
		Description: "All loaded systemd units (`systemctl list-units --output=json`) with " +
			"optional state/type filters. Default cap is 500 entries — narrow with `state` " +
			"(e.g. \"failed\") or `type` (e.g. \"service\", \"timer\") for focused triage.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listUnitsIn) (*mcp.CallToolResult, listUnitsOut, error) {
		args, err := listUnitsArgs(in)
		if err != nil {
			return nil, listUnitsOut{}, err
		}
		stdout, _, err := d.Exec.Run(ctx, "systemctl", args...)
		if err != nil {
			return nil, listUnitsOut{}, fmt.Errorf("systemctl list-units: %w", err)
		}
		var parsed []systemdUnit
		if err := json.Unmarshal(stdout, &parsed); err != nil {
			return nil, listUnitsOut{}, fmt.Errorf("parse systemctl JSON: %w", err)
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 500
		}
		if limit > 2000 {
			return nil, listUnitsOut{}, fmt.Errorf("limit must be <= 2000, got %d", limit)
		}
		if len(parsed) > limit {
			parsed = parsed[:limit]
		}
		return textResult("%d unit(s) matched", len(parsed)),
			listUnitsOut{Count: len(parsed), Units: parsed}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "unit_status",
		Description: "Detail view of a single systemd unit: every property `systemctl show` " +
			"reports, plus a tail of the unit's recent journal. Requires log-observe for journal.",
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
		showOut, _, err := d.Exec.Run(ctx, "systemctl", "show", "--no-pager", in.Unit)
		if err != nil {
			return nil, unitStatusOut{}, fmt.Errorf("systemctl show %s: %w", in.Unit, err)
		}
		props := parseSystemctlShow(string(showOut))
		// Journal is best-effort — log-observe may not be connected.
		jOut, _, jErr := d.Exec.Run(ctx, "journalctl", "--no-pager", "-o", "short-iso",
			"-n", strconv.Itoa(jLines), "--unit", in.Unit)
		journal := ""
		if jErr == nil {
			journal = string(jOut)
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
		Description: "All systemd timers (`systemctl list-timers --output=json`): next/last " +
			"fire times and the unit each one activates.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listTimersIn) (*mcp.CallToolResult, listTimersOut, error) {
		stdout, _, err := d.Exec.Run(ctx, "systemctl", "list-timers", "--no-pager", "--all", "--output=json")
		if err != nil {
			return nil, listTimersOut{}, fmt.Errorf("systemctl list-timers: %w", err)
		}
		var raw []rawSystemdTimer
		if err := json.Unmarshal(stdout, &raw); err != nil {
			return nil, listTimersOut{}, fmt.Errorf("parse systemctl JSON: %w", err)
		}
		parsed := make([]systemdTimer, len(raw))
		for i, r := range raw {
			parsed[i] = systemdTimer{
				NextMicros: r.Next, LeftMicros: r.Left,
				LastMicros: r.Last, PassedMicros: r.Passed,
				Unit: r.Unit, Activates: r.Activates,
			}
		}
		return textResult("%d timer(s)", len(parsed)),
			listTimersOut{Count: len(parsed), Timers: parsed}, nil
	})
}

// listUnitsArgs builds the systemctl argv. Kept pure for unit-testing.
func listUnitsArgs(in listUnitsIn) ([]string, error) {
	args := []string{"list-units", "--no-pager", "--all", "--output=json"}
	if in.State != "" {
		allowed := map[string]bool{
			"active": true, "inactive": true, "failed": true,
			"activating": true, "deactivating": true, "reloading": true,
		}
		if !allowed[in.State] {
			return nil, fmt.Errorf("invalid state %q", in.State)
		}
		args = append(args, "--state="+in.State)
	}
	if in.Type != "" {
		allowed := map[string]bool{
			"service": true, "timer": true, "socket": true, "mount": true,
			"target": true, "path": true, "swap": true, "slice": true,
			"scope": true, "device": true, "automount": true,
		}
		if !allowed[in.Type] {
			return nil, fmt.Errorf("invalid type %q", in.Type)
		}
		args = append(args, "--type="+in.Type)
	}
	return args, nil
}

// parseSystemctlShow consumes the Key=Value\n stream `systemctl show` emits
// and returns a flat string map. Values that contain '=' are preserved
// verbatim after the first delimiter.
func parseSystemctlShow(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}
