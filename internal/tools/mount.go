package tools

import (
	"context"
	"fmt"
	"regexp"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/procfs"
	"github.com/gjolly/fleetmind/internal/snapconf"
)

type listMountsIn struct {
	Fstype          string `json:"fstype,omitempty" jsonschema:"keep only mounts of this fstype (e.g. ext4, btrfs, squashfs)"`
	MountPointRegex string `json:"mount_point_regex,omitempty" jsonschema:"keep only mounts whose mount point matches this regex (max 200 chars)"`
}

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
		Description: "All mount points from /proc/self/mountinfo, enriched with statfs sizes for normal filesystems. Optional filters: fstype, mount_point_regex.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listMountsIn) (*mcp.CallToolResult, listMountsOut, error) {
		var mpRE *regexp.Regexp
		if in.MountPointRegex != "" {
			if len(in.MountPointRegex) > 200 {
				return nil, listMountsOut{}, fmt.Errorf("mount_point_regex too long (%d > 200)", len(in.MountPointRegex))
			}
			re, err := regexp.Compile(in.MountPointRegex)
			if err != nil {
				return nil, listMountsOut{}, fmt.Errorf("mount_point_regex does not compile: %w", err)
			}
			mpRE = re
		}
		// Inside a strictly-confined snap, /proc/self/mountinfo is the snap's
		// confined mount namespace (snap base bind-mounts, tmpfs overlays,
		// every other snap's squashfs). Read PID 1's mountinfo instead — host
		// systemd lives in the host's mount namespace, and the mount-observe
		// plug grants read access to /proc/[pid]/mountinfo.
		var (
			entries []procfs.MountEntry
			err     error
		)
		if snapconf.InSnap() {
			entries, err = d.ProcFS.MountsForPID("1")
		} else {
			entries, err = d.ProcFS.Mounts()
		}
		if err != nil {
			return nil, listMountsOut{}, err
		}
		out := listMountsOut{Mounts: make([]mountOut, 0, len(entries))}
		for _, e := range entries {
			if in.Fstype != "" && e.Type != in.Fstype {
				continue
			}
			if mpRE != nil && !mpRE.MatchString(e.MountPoint) {
				continue
			}
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
