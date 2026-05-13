package sysfs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gjolly/fleetmind/internal/sysfs"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNetInterfaces(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "class", "net", "eth0")
	write(t, filepath.Join(base, "address"), "aa:bb:cc:dd:ee:ff\n")
	write(t, filepath.Join(base, "mtu"), "1500\n")
	write(t, filepath.Join(base, "operstate"), "up\n")
	write(t, filepath.Join(base, "ifindex"), "2\n")
	write(t, filepath.Join(base, "type"), "1\n")
	write(t, filepath.Join(base, "carrier"), "1\n")
	write(t, filepath.Join(base, "speed"), "1000\n")

	got, err := sysfs.NewRoot(dir).NetInterfaces()
	if err != nil {
		t.Fatalf("NetInterfaces: %v", err)
	}
	if len(got) != 1 || got[0].Name != "eth0" {
		t.Fatalf("got = %+v", got)
	}
	if got[0].MTU != 1500 || got[0].IfIndex != 2 || got[0].Address != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("parsed wrong: %+v", got[0])
	}
}

func TestHwmonReadings(t *testing.T) {
	dir := t.TempDir()
	chip := filepath.Join(dir, "class", "hwmon", "hwmon0")
	write(t, filepath.Join(chip, "name"), "coretemp\n")
	write(t, filepath.Join(chip, "temp1_input"), "42000\n")
	write(t, filepath.Join(chip, "temp1_label"), "Package id 0\n")
	write(t, filepath.Join(chip, "fan1_input"), "1500\n")
	write(t, filepath.Join(chip, "in0_input"), "1200\n")

	got, err := sysfs.NewRoot(dir).HwmonReadings()
	if err != nil {
		t.Fatalf("HwmonReadings: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 readings, got %d (%+v)", len(got), got)
	}
	byLabel := map[string]float64{}
	byUnit := map[string]string{}
	for _, r := range got {
		byLabel[r.Sensor] = r.Value
		byUnit[r.Sensor] = r.Unit
	}
	if byLabel["temp1"] != 42.0 || byUnit["temp1"] != "°C" {
		t.Errorf("temp1: %v %s", byLabel["temp1"], byUnit["temp1"])
	}
	if byLabel["fan1"] != 1500 || byUnit["fan1"] != "RPM" {
		t.Errorf("fan1: %v %s", byLabel["fan1"], byUnit["fan1"])
	}
	if byLabel["in0"] != 1.2 || byUnit["in0"] != "V" {
		t.Errorf("in0: %v %s", byLabel["in0"], byUnit["in0"])
	}
}

func TestPCIDevices(t *testing.T) {
	dir := t.TempDir()
	dev := filepath.Join(dir, "bus", "pci", "devices", "0000:00:1f.2")
	write(t, filepath.Join(dev, "vendor"), "0x8086\n")
	write(t, filepath.Join(dev, "device"), "0x9d03\n")
	write(t, filepath.Join(dev, "class"), "0x010601\n")
	write(t, filepath.Join(dev, "revision"), "0x21\n")

	got, err := sysfs.NewRoot(dir).PCIDevices()
	if err != nil {
		t.Fatalf("PCIDevices: %v", err)
	}
	if len(got) != 1 || got[0].Address != "0000:00:1f.2" {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Vendor != "0x8086" || got[0].Device != "0x9d03" {
		t.Errorf("ids wrong: %+v", got[0])
	}
}
