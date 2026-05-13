package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type memoryInfoIn struct{}

type memoryInfoOut struct {
	TotalBytes     uint64            `json:"total_bytes"`
	FreeBytes      uint64            `json:"free_bytes"`
	AvailableBytes uint64            `json:"available_bytes"`
	BuffersBytes   uint64            `json:"buffers_bytes"`
	CachedBytes    uint64            `json:"cached_bytes"`
	SwapTotalBytes uint64            `json:"swap_total_bytes"`
	SwapFreeBytes  uint64            `json:"swap_free_bytes"`
	Raw            map[string]uint64 `json:"raw"`
}

func registerMemory(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "memory_info",
		Description: "RAM and swap utilisation in bytes, parsed from /proc/meminfo.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ memoryInfoIn) (*mcp.CallToolResult, memoryInfoOut, error) {
		m, err := d.ProcFS.MemInfo()
		if err != nil {
			return nil, memoryInfoOut{}, err
		}
		out := memoryInfoOut{
			TotalBytes:     m["MemTotal"],
			FreeBytes:      m["MemFree"],
			AvailableBytes: m["MemAvailable"],
			BuffersBytes:   m["Buffers"],
			CachedBytes:    m["Cached"],
			SwapTotalBytes: m["SwapTotal"],
			SwapFreeBytes:  m["SwapFree"],
			Raw:            m,
		}
		used := out.TotalBytes - out.AvailableBytes
		return textResult("mem %.1f/%.1f GiB used; swap %.1f/%.1f GiB used",
			float64(used)/(1<<30), float64(out.TotalBytes)/(1<<30),
			float64(out.SwapTotalBytes-out.SwapFreeBytes)/(1<<30), float64(out.SwapTotalBytes)/(1<<30)), out, nil
	})
}
