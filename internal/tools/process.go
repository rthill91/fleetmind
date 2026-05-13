package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/procfs"
)

type listProcessesIn struct {
	// Limit caps the number of processes returned (after sorting by RSS desc).
	// 0 means no limit. `top` is an alias kept for ergonomics.
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of entries to return; 0 = unlimited"`
	Top       int    `json:"top,omitempty" jsonschema:"alias for limit; if both set, the smaller wins"`
	NameRegex string `json:"name_regex,omitempty" jsonschema:"keep only processes whose comm matches this regex (max 200 chars)"`
	State     string `json:"state,omitempty" jsonschema:"keep only processes in this /proc state: R|S|D|Z|T|I"`
}

type processOut struct {
	PID     int      `json:"pid"`
	PPID    int      `json:"ppid"`
	Comm    string   `json:"comm"`
	State   string   `json:"state"`
	UID     int      `json:"uid"`
	GID     int      `json:"gid"`
	Threads int      `json:"threads"`
	VMSize  uint64   `json:"vm_size_bytes"`
	VMRSS   uint64   `json:"vm_rss_bytes"`
	CmdLine []string `json:"cmdline"`
}

type listProcessesOut struct {
	Count     int          `json:"count"`
	Processes []processOut `json:"processes"`
}

type getProcessIn struct {
	PID int `json:"pid" jsonschema:"the PID to look up"`
}

func registerProcess(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_processes",
		Description: "Snapshot of all running processes sorted by resident set size (descending). Optional filters: name_regex (matches comm), state (R/S/D/Z/T/I), top/limit.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listProcessesIn) (*mcp.CallToolResult, listProcessesOut, error) {
		var nameRE *regexp.Regexp
		if in.NameRegex != "" {
			if len(in.NameRegex) > 200 {
				return nil, listProcessesOut{}, fmt.Errorf("name_regex too long (%d > 200)", len(in.NameRegex))
			}
			re, err := regexp.Compile(in.NameRegex)
			if err != nil {
				return nil, listProcessesOut{}, fmt.Errorf("name_regex does not compile: %w", err)
			}
			nameRE = re
		}
		if in.State != "" {
			if len(in.State) != 1 || !strings.Contains("RSDZTI", strings.ToUpper(in.State)) {
				return nil, listProcessesOut{}, fmt.Errorf("invalid state %q (expected one of R/S/D/Z/T/I)", in.State)
			}
		}
		ps, err := d.ProcFS.ListProcesses()
		if err != nil {
			return nil, listProcessesOut{}, err
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].VMRSS > ps[j].VMRSS })
		if nameRE != nil || in.State != "" {
			wantState := strings.ToUpper(in.State)
			filtered := ps[:0]
			for _, p := range ps {
				if nameRE != nil && !nameRE.MatchString(p.Comm) {
					continue
				}
				if wantState != "" && !strings.EqualFold(p.State, wantState) {
					continue
				}
				filtered = append(filtered, p)
			}
			ps = filtered
		}
		cap := effectiveLimit(in.Limit, in.Top)
		if cap > 0 && len(ps) > cap {
			ps = ps[:cap]
		}
		out := listProcessesOut{Count: len(ps), Processes: make([]processOut, 0, len(ps))}
		for _, p := range ps {
			out.Processes = append(out.Processes, toProcessOut(p))
		}
		return textResult("%d processes", out.Count), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_process",
		Description: "Look up a single process by PID.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getProcessIn) (*mcp.CallToolResult, processOut, error) {
		if in.PID <= 0 {
			return nil, processOut{}, errors.New("pid must be positive")
		}
		p, err := d.ProcFS.Process(in.PID)
		if err != nil {
			return nil, processOut{}, fmt.Errorf("read pid %d: %w", in.PID, err)
		}
		o := toProcessOut(p)
		return textResult("pid %d (%s) state %s", o.PID, o.Comm, o.State), o, nil
	})
}

// effectiveLimit returns the smaller of the two positive values, or whichever
// is positive when only one is set. Zero/negative inputs mean "no cap" for
// that side; if both are zero, the result is 0 (unlimited).
func effectiveLimit(limit, top int) int {
	switch {
	case limit > 0 && top > 0:
		if top < limit {
			return top
		}
		return limit
	case limit > 0:
		return limit
	case top > 0:
		return top
	default:
		return 0
	}
}

func toProcessOut(p procfs.ProcessSummary) processOut {
	return processOut{
		PID: p.PID, PPID: p.PPID, Comm: p.Comm, State: p.State,
		UID: p.UID, GID: p.GID, Threads: p.Threads,
		VMSize: p.VMSize, VMRSS: p.VMRSS, CmdLine: p.CmdLine,
	}
}
