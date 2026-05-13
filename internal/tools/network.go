package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/exectool"
)

type listNetIfacesIn struct{}

type ifaceOut struct {
	Name      string   `json:"name"`
	Index     int      `json:"index"`
	MAC       string   `json:"mac"`
	MTU       int      `json:"mtu"`
	Type      string   `json:"type"`
	OperState string   `json:"oper_state"`
	Carrier   string   `json:"carrier"`
	Speed     string   `json:"speed"`
	Addrs     []string `json:"addresses"`
}

type listNetIfacesOut struct {
	Count      int        `json:"count"`
	Interfaces []ifaceOut `json:"interfaces"`
}

type listSocketsIn struct {
	// Protocols filters by protocol family. Accepted: "tcp", "udp", "unix".
	// Defaults to all.
	Protocols []string `json:"protocols,omitempty" jsonschema:"subset of tcp,udp,unix"`
	Listening bool     `json:"listening_only,omitempty" jsonschema:"include only listening sockets (TCP/UDP)"`
}

type listSocketsOut struct {
	Raw string `json:"raw"`
}

func registerNetwork(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_network_interfaces",
		Description: "Network interface table merged from /sys/class/net and getifaddrs(3).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listNetIfacesIn) (*mcp.CallToolResult, listNetIfacesOut, error) {
		raws, err := d.SysFS.NetInterfaces()
		if err != nil {
			return nil, listNetIfacesOut{}, err
		}
		addrs := interfaceAddrs()
		out := listNetIfacesOut{Interfaces: make([]ifaceOut, 0, len(raws))}
		for _, r := range raws {
			out.Interfaces = append(out.Interfaces, ifaceOut{
				Name: r.Name, Index: r.IfIndex, MAC: r.Address,
				MTU: r.MTU, Type: r.Type, OperState: r.OperState,
				Carrier: r.Carrier, Speed: r.Speed, Addrs: addrs[r.Name],
			})
		}
		out.Count = len(out.Interfaces)
		return textResult("%d interfaces", out.Count), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_sockets",
		Description: "Socket inventory via `ss`. Set listening_only to filter, or protocols to scope to tcp/udp/unix.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listSocketsIn) (*mcp.CallToolResult, listSocketsOut, error) {
		args, err := ssArgs(in)
		if err != nil {
			return nil, listSocketsOut{}, err
		}
		stdout, _, err := d.Exec.Run(ctx, "ss", args...)
		if err != nil {
			if errors.Is(err, exectool.ErrOutputTooLarge) {
				return nil, listSocketsOut{}, err
			}
			return nil, listSocketsOut{}, fmt.Errorf("ss: %w", err)
		}
		return textResult("ss returned %d bytes", len(stdout)), listSocketsOut{Raw: string(stdout)}, nil
	})
}

func ssArgs(in listSocketsIn) ([]string, error) {
	args := []string{"-n", "-p"}
	if in.Listening {
		args = append(args, "-l")
	} else {
		args = append(args, "-a")
	}
	families := map[string]string{"tcp": "-t", "udp": "-u", "unix": "-x"}
	if len(in.Protocols) == 0 {
		args = append(args, "-t", "-u", "-x")
	} else {
		for _, p := range in.Protocols {
			f, ok := families[strings.ToLower(p)]
			if !ok {
				return nil, fmt.Errorf("unsupported protocol %q (allowed: tcp, udp, unix)", p)
			}
			args = append(args, f)
		}
	}
	return args, nil
}

// interfaceAddrs returns interface-name → list of IP/CIDR strings.
func interfaceAddrs() map[string][]string {
	out := map[string][]string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		a, err := ifc.Addrs()
		if err != nil {
			continue
		}
		s := make([]string, 0, len(a))
		for _, addr := range a {
			s = append(s, addr.String())
		}
		out[ifc.Name] = s
	}
	return out
}
