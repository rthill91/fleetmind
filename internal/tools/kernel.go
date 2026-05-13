package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type kernelInfoIn struct{}

type kernelInfoOut struct {
	Version string            `json:"version"`
	CmdLine string            `json:"cmdline"`
	Sysctl  map[string]string `json:"sysctl"`
}

type moduleOut struct {
	Name       string   `json:"name"`
	SizeBytes  uint64   `json:"size_bytes"`
	UsedBy     int      `json:"used_by_count"`
	Dependents []string `json:"dependents"`
	State      string   `json:"state"`
}

type listModulesIn struct{}

type listModulesOut struct {
	Count   int         `json:"count"`
	Modules []moduleOut `json:"modules"`
}

func registerKernel(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "kernel_info",
		Description: "Kernel build banner, boot command line and selected /proc/sys/kernel sysctls.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ kernelInfoIn) (*mcp.CallToolResult, kernelInfoOut, error) {
		ki, err := d.ProcFS.KernelInfo()
		if err != nil {
			return nil, kernelInfoOut{}, err
		}
		return textResult("kernel %s", ki.Sysctl["osrelease"]),
			kernelInfoOut{Version: ki.Version, CmdLine: ki.CmdLine, Sysctl: ki.Sysctl}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_kernel_modules",
		Description: "Loaded kernel modules, sizes and dependent modules (from /proc/modules).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listModulesIn) (*mcp.CallToolResult, listModulesOut, error) {
		mods, err := d.ProcFS.Modules()
		if err != nil {
			return nil, listModulesOut{}, err
		}
		out := listModulesOut{Modules: make([]moduleOut, 0, len(mods))}
		for _, m := range mods {
			out.Modules = append(out.Modules, moduleOut{
				Name: m.Name, SizeBytes: m.Size, UsedBy: m.UsedBy,
				Dependents: m.Dependents, State: m.State,
			})
		}
		out.Count = len(out.Modules)
		return textResult("%d kernel modules", out.Count), out, nil
	})
}
