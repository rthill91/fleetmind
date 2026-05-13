package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/procfs"
)

type listProcessesIn struct {
	// Limit caps the number of processes returned (after sorting by RSS desc).
	// 0 means no limit.
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of entries to return; 0 = unlimited"`
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
		Description: "Snapshot of all running processes sorted by resident set size (descending).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listProcessesIn) (*mcp.CallToolResult, listProcessesOut, error) {
		ps, err := d.ProcFS.ListProcesses()
		if err != nil {
			return nil, listProcessesOut{}, err
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].VMRSS > ps[j].VMRSS })
		if in.Limit > 0 && len(ps) > in.Limit {
			ps = ps[:in.Limit]
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

func toProcessOut(p procfs.ProcessSummary) processOut {
	return processOut{
		PID: p.PID, PPID: p.PPID, Comm: p.Comm, State: p.State,
		UID: p.UID, GID: p.GID, Threads: p.Threads,
		VMSize: p.VMSize, VMRSS: p.VMRSS, CmdLine: p.CmdLine,
	}
}
