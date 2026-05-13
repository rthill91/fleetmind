package tools

import (
	"context"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listMountsIn struct{}

type mountOut struct {
	MountPoint  string `json:"mount_point"`
	Source      string `json:"source"`
	Type        string `json:"type"`
	Options     string `json:"options"`
	SizeBytes   uint64 `json:"size_bytes"`
	FreeBytes   uint64 `json:"free_bytes"`
	UsedBytes   uint64 `json:"used_bytes"`
	InodesTotal uint64 `json:"inodes_total"`
	InodesFree  uint64 `json:"inodes_free"`
}

type listMountsOut struct {
	Count  int        `json:"count"`
	Mounts []mountOut `json:"mounts"`
}

func registerMount(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_mounts",
		Description: "All mount points from /proc/self/mountinfo, enriched with statfs sizes for normal filesystems.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listMountsIn) (*mcp.CallToolResult, listMountsOut, error) {
		entries, err := d.ProcFS.Mounts()
		if err != nil {
			return nil, listMountsOut{}, err
		}
		out := listMountsOut{Mounts: make([]mountOut, 0, len(entries))}
		for _, e := range entries {
			m := mountOut{
				MountPoint: e.MountPoint, Source: e.Source,
				Type: e.Type, Options: e.Options,
			}
			var st syscall.Statfs_t
			if err := syscall.Statfs(e.MountPoint, &st); err == nil && st.Blocks > 0 {
				bsize := uint64(st.Bsize)
				m.SizeBytes = st.Blocks * bsize
				m.FreeBytes = st.Bavail * bsize
				m.UsedBytes = (st.Blocks - st.Bfree) * bsize
				m.InodesTotal = st.Files
				m.InodesFree = st.Ffree
			}
			out.Mounts = append(out.Mounts, m)
		}
		out.Count = len(out.Mounts)
		return textResult("%d mounts", out.Count), out, nil
	})
}
