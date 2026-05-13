package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listPCIIn struct{}

type pciOut struct {
	Address  string `json:"address"`
	VendorID string `json:"vendor_id"`
	DeviceID string `json:"device_id"`
	Class    string `json:"class"`
	Revision string `json:"revision"`
	Driver   string `json:"driver"`
}

type listPCIOut struct {
	Count   int      `json:"count"`
	Devices []pciOut `json:"devices"`
}

func registerPCI(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_pci_devices",
		Description: "PCI devices from /sys/bus/pci with vendor/device IDs (hex) and bound kernel driver.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listPCIIn) (*mcp.CallToolResult, listPCIOut, error) {
		devs, err := d.SysFS.PCIDevices()
		if err != nil {
			return nil, listPCIOut{}, err
		}
		out := listPCIOut{Devices: make([]pciOut, 0, len(devs))}
		for _, dev := range devs {
			out.Devices = append(out.Devices, pciOut{
				Address: dev.Address, VendorID: dev.Vendor, DeviceID: dev.Device,
				Class: dev.Class, Revision: dev.Revision, Driver: dev.Driver,
			})
		}
		out.Count = len(out.Devices)
		return textResult("%d PCI devices", out.Count), out, nil
	})
}
