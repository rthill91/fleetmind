// Package sysfs contains read-only sysfs/DMI/hwmon helpers. Like procfs, it is
// a thin parsing layer; nothing here mutates state.
package sysfs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Root points at a sysfs mount. The default is /sys.
type Root struct{ Path string }

// Default is the system sysfs at /sys.
var Default = Root{Path: "/sys"}

// NewRoot returns a Root rooted at the given path. Useful for tests.
func NewRoot(path string) Root { return Root{Path: path} }

func (r Root) file(parts ...string) string {
	return filepath.Join(append([]string{r.Path}, parts...)...)
}

// readTrimmed reads a sysfs attribute and returns its trimmed contents. An
// empty string is returned if the file does not exist; this matches sysfs
// idiom where many attributes are optional.
func readTrimmed(path string) string {
	// path is always constructed from a Root.file(...) join inside this
	// package, so it can never reach a caller-controlled location.
	b, err := os.ReadFile(path) //nolint:gosec // path comes from a fixed sysfs prefix
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// NetIface is one entry of /sys/class/net/*.
type NetIface struct {
	Name      string
	Address   string
	MTU       int
	OperState string
	Speed     string
	Type      string
	Carrier   string
	IfIndex   int
}

// NetInterfaces enumerates /sys/class/net/*.
func (r Root) NetInterfaces() ([]NetIface, error) {
	base := r.file("class", "net")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	out := make([]NetIface, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		dir := filepath.Join(base, name)
		i := NetIface{
			Name:      name,
			Address:   readTrimmed(filepath.Join(dir, "address")),
			OperState: readTrimmed(filepath.Join(dir, "operstate")),
			Speed:     readTrimmed(filepath.Join(dir, "speed")),
			Type:      readTrimmed(filepath.Join(dir, "type")),
			Carrier:   readTrimmed(filepath.Join(dir, "carrier")),
		}
		i.MTU, _ = strconv.Atoi(readTrimmed(filepath.Join(dir, "mtu")))
		i.IfIndex, _ = strconv.Atoi(readTrimmed(filepath.Join(dir, "ifindex")))
		out = append(out, i)
	}
	return out, nil
}

// DMI is the system identification block under /sys/class/dmi/id.
type DMI struct {
	SysVendor      string
	ProductName    string
	ProductVersion string
	ProductSerial  string
	ProductUUID    string
	BoardVendor    string
	BoardName      string
	BoardVersion   string
	BiosVendor     string
	BiosVersion    string
	BiosDate       string
	ChassisVendor  string
	ChassisType    string
	ChassisVersion string
}

// ReadDMI returns the DMI identification block. Fields the kernel does not
// expose (or that require privilege) come back empty.
func (r Root) ReadDMI() (DMI, error) {
	base := r.file("class", "dmi", "id")
	if _, err := os.Stat(base); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DMI{}, nil
		}
		return DMI{}, err
	}
	return DMI{
		SysVendor:      readTrimmed(filepath.Join(base, "sys_vendor")),
		ProductName:    readTrimmed(filepath.Join(base, "product_name")),
		ProductVersion: readTrimmed(filepath.Join(base, "product_version")),
		ProductSerial:  readTrimmed(filepath.Join(base, "product_serial")),
		ProductUUID:    readTrimmed(filepath.Join(base, "product_uuid")),
		BoardVendor:    readTrimmed(filepath.Join(base, "board_vendor")),
		BoardName:      readTrimmed(filepath.Join(base, "board_name")),
		BoardVersion:   readTrimmed(filepath.Join(base, "board_version")),
		BiosVendor:     readTrimmed(filepath.Join(base, "bios_vendor")),
		BiosVersion:    readTrimmed(filepath.Join(base, "bios_version")),
		BiosDate:       readTrimmed(filepath.Join(base, "bios_date")),
		ChassisVendor:  readTrimmed(filepath.Join(base, "chassis_vendor")),
		ChassisType:    readTrimmed(filepath.Join(base, "chassis_type")),
		ChassisVersion: readTrimmed(filepath.Join(base, "chassis_version")),
	}, nil
}

// PCIDevice is a single entry under /sys/bus/pci/devices.
type PCIDevice struct {
	Address  string // BDF, e.g. 0000:00:1f.2
	Vendor   string // hex e.g. 0x8086
	Device   string
	Class    string
	Revision string
	Driver   string
}

// PCIDevices enumerates /sys/bus/pci/devices.
func (r Root) PCIDevices() ([]PCIDevice, error) {
	base := r.file("bus", "pci", "devices")
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]PCIDevice, 0, len(entries))
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		d := PCIDevice{
			Address:  e.Name(),
			Vendor:   readTrimmed(filepath.Join(dir, "vendor")),
			Device:   readTrimmed(filepath.Join(dir, "device")),
			Class:    readTrimmed(filepath.Join(dir, "class")),
			Revision: readTrimmed(filepath.Join(dir, "revision")),
		}
		if link, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
			d.Driver = filepath.Base(link)
		}
		out = append(out, d)
	}
	return out, nil
}

// USBDevice is a single entry under /sys/bus/usb/devices (excluding interface
// children — only top-level devices are returned).
type USBDevice struct {
	Path         string // sysfs name e.g. 1-1
	IDVendor     string
	IDProduct    string
	Manufacturer string
	Product      string
	Serial       string
	BusNum       int
	DevNum       int
	Speed        string
}

// USBDevices enumerates /sys/bus/usb/devices, filtering to entries that
// expose idVendor (i.e. real devices, not interface nodes).
func (r Root) USBDevices() ([]USBDevice, error) {
	base := r.file("bus", "usb", "devices")
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]USBDevice, 0, len(entries))
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		vendor := readTrimmed(filepath.Join(dir, "idVendor"))
		if vendor == "" {
			continue
		}
		d := USBDevice{
			Path:         e.Name(),
			IDVendor:     vendor,
			IDProduct:    readTrimmed(filepath.Join(dir, "idProduct")),
			Manufacturer: readTrimmed(filepath.Join(dir, "manufacturer")),
			Product:      readTrimmed(filepath.Join(dir, "product")),
			Serial:       readTrimmed(filepath.Join(dir, "serial")),
			Speed:        readTrimmed(filepath.Join(dir, "speed")),
		}
		d.BusNum, _ = strconv.Atoi(readTrimmed(filepath.Join(dir, "busnum")))
		d.DevNum, _ = strconv.Atoi(readTrimmed(filepath.Join(dir, "devnum")))
		out = append(out, d)
	}
	return out, nil
}

// HwmonReading is one labelled reading from /sys/class/hwmon.
type HwmonReading struct {
	Chip   string  // e.g. "coretemp", "nvme"
	Label  string  // e.g. "Package id 0" (falls back to attribute name)
	Sensor string  // e.g. temp1, fan2, in0
	Value  float64 // milli-units scaled to base units (°C, V, RPM)
	Unit   string
}

// HwmonReadings enumerates hwmon-exposed sensors.
func (r Root) HwmonReadings() ([]HwmonReading, error) {
	base := r.file("class", "hwmon")
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []HwmonReading
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		chip := readTrimmed(filepath.Join(dir, "name"))
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, "_input") {
				continue
			}
			sensor := strings.TrimSuffix(name, "_input")
			raw := readTrimmed(filepath.Join(dir, name))
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			label := readTrimmed(filepath.Join(dir, sensor+"_label"))
			if label == "" {
				label = sensor
			}
			val, unit := scaleHwmon(sensor, n)
			out = append(out, HwmonReading{Chip: chip, Label: label, Sensor: sensor, Value: val, Unit: unit})
		}
	}
	return out, nil
}

// scaleHwmon converts hwmon's milli-units into base units when sensible.
func scaleHwmon(sensor string, raw float64) (float64, string) {
	switch {
	case strings.HasPrefix(sensor, "temp"):
		return raw / 1000.0, "°C"
	case strings.HasPrefix(sensor, "in"):
		return raw / 1000.0, "V"
	case strings.HasPrefix(sensor, "curr"):
		return raw / 1000.0, "A"
	case strings.HasPrefix(sensor, "power"):
		return raw / 1_000_000.0, "W"
	case strings.HasPrefix(sensor, "energy"):
		return raw / 1_000_000.0, "J"
	case strings.HasPrefix(sensor, "fan"):
		return raw, "RPM"
	default:
		return raw, ""
	}
}
