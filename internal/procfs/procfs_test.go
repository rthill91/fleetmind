package procfs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gjolly/fleetmind/internal/procfs"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestMemInfo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meminfo"), `MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    8192000 kB
Hugepagesize:       2048 kB
HugePages_Total:       0
`)
	got, err := procfs.NewRoot(dir).MemInfo()
	if err != nil {
		t.Fatalf("MemInfo: %v", err)
	}
	if got["MemTotal"] != 16384000*1024 {
		t.Errorf("MemTotal = %d, want %d", got["MemTotal"], 16384000*1024)
	}
	if got["MemAvailable"] != 8192000*1024 {
		t.Errorf("MemAvailable = %d", got["MemAvailable"])
	}
	if got["HugePages_Total"] != 0 {
		t.Errorf("HugePages_Total = %d", got["HugePages_Total"])
	}
}

func TestLoadAvg(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "loadavg"), "0.51 0.42 0.30 3/512 99124\n")
	got, err := procfs.NewRoot(dir).LoadAvg()
	if err != nil {
		t.Fatalf("LoadAvg: %v", err)
	}
	if got.One != 0.51 || got.Five != 0.42 || got.Fifteen != 0.30 {
		t.Errorf("averages wrong: %+v", got)
	}
	if got.Running != 3 || got.Total != 512 {
		t.Errorf("tasks wrong: %+v", got)
	}
	if got.LastPID != 99124 {
		t.Errorf("LastPID = %d", got.LastPID)
	}
}

func TestModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "modules"), `nf_conntrack 196608 4 nf_nat,iptable_nat, Live 0x0000000000000000
ext4 970752 2 - Live 0x0000000000000000
`)
	mods, err := procfs.NewRoot(dir).Modules()
	if err != nil {
		t.Fatalf("Modules: %v", err)
	}
	if len(mods) != 2 {
		t.Fatalf("want 2 modules, got %d", len(mods))
	}
	if mods[0].Name != "nf_conntrack" || mods[0].UsedBy != 4 {
		t.Errorf("nf_conntrack parsed wrong: %+v", mods[0])
	}
	if len(mods[0].Dependents) != 2 {
		t.Errorf("nf_conntrack dependents = %v", mods[0].Dependents)
	}
	if mods[1].Name != "ext4" || len(mods[1].Dependents) != 0 {
		t.Errorf("ext4 parsed wrong: %+v", mods[1])
	}
}

func TestMounts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "self", "mountinfo"),
		"23 28 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw\n"+
			"24 28 0:22 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw\n")
	m, err := procfs.NewRoot(dir).Mounts()
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("want 2 mounts, got %d", len(m))
	}
	if m[0].MountPoint != "/proc" || m[0].Type != "proc" {
		t.Errorf("/proc parsed wrong: %+v", m[0])
	}
	if m[1].MountPoint != "/sys" || m[1].Type != "sysfs" {
		t.Errorf("/sys parsed wrong: %+v", m[1])
	}
}

func TestCPUInfo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cpuinfo"), `processor	: 0
vendor_id	: GenuineIntel
model name	: Test CPU
cpu MHz		: 3200.000
flags		: fpu vme

processor	: 1
vendor_id	: GenuineIntel
model name	: Test CPU
cpu MHz		: 1600.000
flags		: fpu vme
`)
	entries, err := procfs.NewRoot(dir).CPUInfo()
	if err != nil {
		t.Fatalf("CPUInfo: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].Fields["model name"] != "Test CPU" {
		t.Errorf("model name parsed wrong: %v", entries[0].Fields)
	}
}
