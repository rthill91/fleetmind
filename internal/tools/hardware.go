package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listDMIIn struct{}

type dmiOut struct {
	SysVendor      string `json:"sys_vendor"`
	ProductName    string `json:"product_name"`
	ProductVersion string `json:"product_version"`
	ProductSerial  string `json:"product_serial"`
	ProductUUID    string `json:"product_uuid"`
	BoardVendor    string `json:"board_vendor"`
	BoardName      string `json:"board_name"`
	BoardVersion   string `json:"board_version"`
	BiosVendor     string `json:"bios_vendor"`
	BiosVersion    string `json:"bios_version"`
	BiosDate       string `json:"bios_date"`
	ChassisVendor  string `json:"chassis_vendor"`
	ChassisType    string `json:"chassis_type"`
	ChassisVersion string `json:"chassis_version"`
}

type listSensorsIn struct{}

type sensorOut struct {
	Chip   string  `json:"chip"`
	Label  string  `json:"label"`
	Sensor string  `json:"sensor"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
}

type listSensorsOut struct {
	Count   int         `json:"count"`
	Sensors []sensorOut `json:"sensors"`
}

func registerHardware(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_dmi",
		Description: "SMBIOS/DMI identification (vendor, product, board, BIOS, chassis) from /sys/class/dmi/id.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listDMIIn) (*mcp.CallToolResult, dmiOut, error) {
		dmi, err := d.SysFS.ReadDMI()
		if err != nil {
			return nil, dmiOut{}, err
		}
		out := dmiOut(dmi)
		return textResult("%s %s (BIOS %s %s)",
			out.SysVendor, out.ProductName, out.BiosVendor, out.BiosVersion), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_sensors",
		Description: "Hwmon readings: temperatures (°C), voltages (V), currents (A), power (W), fan speeds (RPM).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listSensorsIn) (*mcp.CallToolResult, listSensorsOut, error) {
		readings, err := d.SysFS.HwmonReadings()
		if err != nil {
			return nil, listSensorsOut{}, err
		}
		out := listSensorsOut{Sensors: make([]sensorOut, 0, len(readings))}
		for _, r := range readings {
			out.Sensors = append(out.Sensors, sensorOut{
				Chip: r.Chip, Label: r.Label, Sensor: r.Sensor, Value: r.Value, Unit: r.Unit,
			})
		}
		out.Count = len(out.Sensors)
		return textResult("%d sensor readings", out.Count), out, nil
	})
}
