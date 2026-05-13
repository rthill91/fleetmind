// Package tools registers all FleetMind MCP tools on an *mcp.Server. Each
// tool lives in its own file; this file owns the shared dependency struct and
// the RegisterAll entry point.
package tools

import (
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/exectool"
	"github.com/gjolly/fleetmind/internal/fleet"
	"github.com/gjolly/fleetmind/internal/procfs"
	"github.com/gjolly/fleetmind/internal/sysfs"
)

// Deps bundles shared collaborators handed to each tool registration. Tools
// own no global state; the runner, procfs and sysfs roots can all be swapped
// in tests.
type Deps struct {
	Exec   *exectool.Runner
	ProcFS procfs.Root
	SysFS  sysfs.Root
	Logger *slog.Logger
	// Fleet is the local fleet registry. Nil when fleet mode is disabled —
	// list_fleet is still registered but reports an empty roster.
	Fleet *fleet.Registry
}

// AllToolNames lists every MCP tool name RegisterAll attaches. Kept in sync
// with the register* calls below; published to fleet peers as part of the
// local node's identity.
var AllToolNames = []string{
	"system_info",
	"cpu_info",
	"memory_info",
	"load_info",
	"list_processes",
	"get_process",
	"list_block_devices",
	"list_mounts",
	"list_network_interfaces",
	"list_sockets",
	"list_pci_devices",
	"list_usb_devices",
	"kernel_info",
	"list_kernel_modules",
	"list_dmi",
	"list_sensors",
	"read_journal",
	"read_dmesg",
	"list_fleet",
}

// RegisterAll attaches every tool to s.
func RegisterAll(s *mcp.Server, d Deps) {
	registerSystem(s, d)
	registerCPU(s, d)
	registerMemory(s, d)
	registerLoad(s, d)
	registerProcess(s, d)
	registerBlock(s, d)
	registerMount(s, d)
	registerNetwork(s, d)
	registerPCI(s, d)
	registerUSB(s, d)
	registerKernel(s, d)
	registerHardware(s, d)
	registerLogs(s, d)
	registerFleet(s, d)
}

// textResult builds an MCP CallToolResult with a single textual summary line.
// Tools also return a typed output struct that clients with structured-output
// support can consume directly.
func textResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
