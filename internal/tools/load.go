package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type loadInfoIn struct{}

type loadInfoOut struct {
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	Load15       float64 `json:"load15"`
	RunningTasks int     `json:"running_tasks"`
	TotalTasks   int     `json:"total_tasks"`
	UptimeSec    float64 `json:"uptime_seconds"`
	IdleSec      float64 `json:"idle_seconds"`
}

func registerLoad(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "load_info",
		Description: "Load averages, running/total tasks, uptime and cumulative idle time.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ loadInfoIn) (*mcp.CallToolResult, loadInfoOut, error) {
		la, err := d.ProcFS.LoadAvg()
		if err != nil {
			return nil, loadInfoOut{}, err
		}
		up, idle, _ := d.ProcFS.Uptime()
		out := loadInfoOut{
			Load1: la.One, Load5: la.Five, Load15: la.Fifteen,
			RunningTasks: la.Running, TotalTasks: la.Total,
			UptimeSec: up, IdleSec: idle,
		}
		return textResult("load %.2f %.2f %.2f · %d/%d tasks · up %.0fs",
			la.One, la.Five, la.Fifteen, la.Running, la.Total, up), out, nil
	})
}
