package tools

import (
	"bufio"
	"context"
	"os"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type systemInfoIn struct{}

type systemInfoOut struct {
	Hostname     string            `json:"hostname"`
	Kernel       string            `json:"kernel"`
	KernelRel    string            `json:"kernel_release"`
	Architecture string            `json:"architecture"`
	OSRelease    map[string]string `json:"os_release"`
	UptimeSec    float64           `json:"uptime_seconds"`
}

func registerSystem(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "system_info",
		Description: "Identification of the running Linux system: hostname, kernel, architecture and parsed /etc/os-release.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ systemInfoIn) (*mcp.CallToolResult, systemInfoOut, error) {
		out := systemInfoOut{OSRelease: map[string]string{}}

		var u syscall.Utsname
		if err := syscall.Uname(&u); err == nil {
			out.Hostname = utsString(u.Nodename[:])
			out.Kernel = utsString(u.Sysname[:]) + " " + utsString(u.Release[:])
			out.KernelRel = utsString(u.Release[:])
			out.Architecture = utsString(u.Machine[:])
		}
		if h, err := os.Hostname(); err == nil && out.Hostname == "" {
			out.Hostname = h
		}
		out.OSRelease = readOSRelease()
		up, _, _ := d.ProcFS.Uptime()
		out.UptimeSec = up

		return textResult("%s — %s (kernel %s, up %.0fs)",
			out.Hostname, out.OSRelease["PRETTY_NAME"], out.KernelRel, out.UptimeSec), out, nil
	})
}

func utsString(b []int8) string {
	bs := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		bs = append(bs, byte(c))
	}
	return string(bs)
}

func readOSRelease() map[string]string {
	out := map[string]string{}
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		f, err := os.Open(p) //nolint:gosec // fixed allowlist of os-release paths
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			out[k] = strings.Trim(v, `"`)
		}
		_ = f.Close()
		return out
	}
	return out
}
