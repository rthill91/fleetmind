package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type cpuInfoIn struct{}

type cpuInfoOut struct {
	LogicalCores int      `json:"logical_cores"`
	ModelName    string   `json:"model_name"`
	Vendor       string   `json:"vendor_id"`
	CPUFamily    string   `json:"cpu_family"`
	MicrocodeRev string   `json:"microcode"`
	Flags        []string `json:"flags"`
	MHzMin       float64  `json:"mhz_min"`
	MHzMax       float64  `json:"mhz_max"`
}

func registerCPU(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "cpu_info",
		Description: "Aggregate CPU view: vendor, model, logical core count, flags and frequency bounds (from /proc/cpuinfo).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ cpuInfoIn) (*mcp.CallToolResult, cpuInfoOut, error) {
		entries, err := d.ProcFS.CPUInfo()
		if err != nil {
			return nil, cpuInfoOut{}, err
		}
		out := cpuInfoOut{LogicalCores: len(entries)}
		for _, e := range entries {
			if out.ModelName == "" {
				out.ModelName = e.Fields["model name"]
			}
			if out.Vendor == "" {
				out.Vendor = e.Fields["vendor_id"]
			}
			if out.CPUFamily == "" {
				out.CPUFamily = e.Fields["cpu family"]
			}
			if out.MicrocodeRev == "" {
				out.MicrocodeRev = e.Fields["microcode"]
			}
			if mhz := parseFloatField(e.Fields["cpu MHz"]); mhz > 0 {
				if out.MHzMin == 0 || mhz < out.MHzMin {
					out.MHzMin = mhz
				}
				if mhz > out.MHzMax {
					out.MHzMax = mhz
				}
			}
			if len(out.Flags) == 0 && e.Fields["flags"] != "" {
				out.Flags = splitFields(e.Fields["flags"])
			}
		}
		return textResult("%d cores · %s (%s)", out.LogicalCores, out.ModelName, out.Vendor), out, nil
	})
}
