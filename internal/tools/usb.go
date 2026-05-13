package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listUSBIn struct{}

type usbOut struct {
	Path         string `json:"sysfs_path"`
	IDVendor     string `json:"id_vendor"`
	IDProduct    string `json:"id_product"`
	Manufacturer string `json:"manufacturer"`
	Product      string `json:"product"`
	Serial       string `json:"serial"`
	BusNum       int    `json:"busnum"`
	DevNum       int    `json:"devnum"`
	Speed        string `json:"speed_mbps"`
}

type listUSBOut struct {
	Count   int      `json:"count"`
	Devices []usbOut `json:"devices"`
}

func registerUSB(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_usb_devices",
		Description: "USB device tree from /sys/bus/usb (top-level devices only; interface nodes are skipped).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listUSBIn) (*mcp.CallToolResult, listUSBOut, error) {
		devs, err := d.SysFS.USBDevices()
		if err != nil {
			return nil, listUSBOut{}, err
		}
		out := listUSBOut{Devices: make([]usbOut, 0, len(devs))}
		for _, dev := range devs {
			out.Devices = append(out.Devices, usbOut{
				Path: dev.Path, IDVendor: dev.IDVendor, IDProduct: dev.IDProduct,
				Manufacturer: dev.Manufacturer, Product: dev.Product, Serial: dev.Serial,
				BusNum: dev.BusNum, DevNum: dev.DevNum, Speed: dev.Speed,
			})
		}
		out.Count = len(out.Devices)
		return textResult("%d USB devices", out.Count), out, nil
	})
}
