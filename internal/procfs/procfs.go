// Package procfs contains small, allocation-modest parsers for the parts of
// /proc this server exposes. Nothing here writes; everything assumes a Linux
// procfs at /proc (overridable via NewRoot for tests).
package procfs

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Root points at a procfs mount. The default is /proc; tests override via
// NewRoot.
type Root struct{ Path string }

// Default is the system procfs at /proc.
var Default = Root{Path: "/proc"}

// NewRoot returns a Root rooted at the given path. Useful for tests.
func NewRoot(path string) Root { return Root{Path: path} }

func (r Root) file(parts ...string) string {
	return filepath.Join(append([]string{r.Path}, parts...)...)
}

// KV represents a key/value line from /proc/meminfo, /proc/loadavg, etc.
type KV struct {
	Key   string
	Value string
}

// MemInfo parses /proc/meminfo into a map of bytes (suffixes resolved).
// Values without a unit are returned in their native units (counts).
func (r Root) MemInfo() (map[string]uint64, error) {
	f, err := os.Open(r.file("meminfo"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	out := make(map[string]uint64, 64)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		rest := strings.TrimSpace(line[colon+1:])
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, perr := strconv.ParseUint(fields[0], 10, 64)
		if perr != nil {
			continue
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			n *= 1024
		}
		out[key] = n
	}
	return out, sc.Err()
}

// LoadAvg holds the three load averages and the running/total tasks fields.
type LoadAvg struct {
	One, Five, Fifteen float64
	Running, Total     int
	LastPID            int
}

// LoadAvg parses /proc/loadavg.
func (r Root) LoadAvg() (LoadAvg, error) {
	b, err := os.ReadFile(r.file("loadavg"))
	if err != nil {
		return LoadAvg{}, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 5 {
		return LoadAvg{}, fmt.Errorf("loadavg: unexpected format %q", b)
	}
	la := LoadAvg{}
	la.One, _ = strconv.ParseFloat(fields[0], 64)
	la.Five, _ = strconv.ParseFloat(fields[1], 64)
	la.Fifteen, _ = strconv.ParseFloat(fields[2], 64)
	if slash := strings.IndexByte(fields[3], '/'); slash > 0 {
		la.Running, _ = strconv.Atoi(fields[3][:slash])
		la.Total, _ = strconv.Atoi(fields[3][slash+1:])
	}
	la.LastPID, _ = strconv.Atoi(fields[4])
	return la, nil
}

// Uptime returns (uptime, idle) in seconds from /proc/uptime.
func (r Root) Uptime() (uptime, idle float64, err error) {
	b, err := os.ReadFile(r.file("uptime"))
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("uptime: unexpected format %q", b)
	}
	uptime, _ = strconv.ParseFloat(fields[0], 64)
	idle, _ = strconv.ParseFloat(fields[1], 64)
	return uptime, idle, nil
}

// CPUInfoEntry is one processor block from /proc/cpuinfo.
type CPUInfoEntry struct {
	Processor int
	Fields    map[string]string
}

// CPUInfo parses /proc/cpuinfo into per-processor blocks. Blocks are separated
// by blank lines in the kernel's output.
func (r Root) CPUInfo() ([]CPUInfoEntry, error) {
	f, err := os.Open(r.file("cpuinfo"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []CPUInfoEntry
	cur := CPUInfoEntry{Processor: -1, Fields: map[string]string{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(cur.Fields) > 0 {
				out = append(out, cur)
				cur = CPUInfoEntry{Processor: -1, Fields: map[string]string{}}
			}
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		k := strings.TrimSpace(line[:colon])
		v := strings.TrimSpace(line[colon+1:])
		cur.Fields[k] = v
		if k == "processor" {
			cur.Processor, _ = strconv.Atoi(v)
		}
	}
	if len(cur.Fields) > 0 {
		out = append(out, cur)
	}
	return out, sc.Err()
}

// Module is a single line from /proc/modules.
type Module struct {
	Name       string
	Size       uint64
	UsedBy     int
	Dependents []string
	State      string
	Offset     uint64
}

// Modules parses /proc/modules.
func (r Root) Modules() ([]Module, error) {
	f, err := os.Open(r.file("modules"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Module
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		m := Module{Name: fields[0]}
		m.Size, _ = strconv.ParseUint(fields[1], 10, 64)
		m.UsedBy, _ = strconv.Atoi(fields[2])
		if fields[3] != "-" {
			m.Dependents = strings.Split(strings.TrimSuffix(fields[3], ","), ",")
		}
		if len(fields) > 4 {
			m.State = fields[4]
		}
		if len(fields) > 5 {
			m.Offset, _ = strconv.ParseUint(fields[5], 0, 64)
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// MountEntry is one line from /proc/self/mountinfo.
type MountEntry struct {
	MountID, ParentID  int
	DevMajor, DevMinor int
	Root               string
	MountPoint         string
	Options            string
	OptionalFields     []string
	Type               string
	Source             string
	SuperOptions       string
}

// Mounts parses /proc/self/mountinfo following the format described in
// Documentation/filesystems/proc.rst.
func (r Root) Mounts() ([]MountEntry, error) {
	f, err := os.Open(r.file("self", "mountinfo"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []MountEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		dash := -1
		for i, f := range fields {
			if f == "-" {
				dash = i
				break
			}
		}
		if dash < 0 || dash+2 >= len(fields) {
			continue
		}
		m := MountEntry{}
		m.MountID, _ = strconv.Atoi(fields[0])
		m.ParentID, _ = strconv.Atoi(fields[1])
		if maj, min, ok := strings.Cut(fields[2], ":"); ok {
			m.DevMajor, _ = strconv.Atoi(maj)
			m.DevMinor, _ = strconv.Atoi(min)
		}
		m.Root = fields[3]
		m.MountPoint = fields[4]
		m.Options = fields[5]
		m.OptionalFields = append([]string(nil), fields[6:dash]...)
		m.Type = fields[dash+1]
		m.Source = fields[dash+2]
		if dash+3 < len(fields) {
			m.SuperOptions = fields[dash+3]
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// ProcessSummary is the bare-minimum process snapshot from /proc/[pid].
type ProcessSummary struct {
	PID     int
	PPID    int
	Comm    string
	State   string
	UID     int
	GID     int
	CmdLine []string
	Threads int
	VMSize  uint64 // bytes
	VMRSS   uint64 // bytes
}

// ListProcesses enumerates numeric /proc/[pid] entries.
func (r Root) ListProcesses() ([]ProcessSummary, error) {
	entries, err := os.ReadDir(r.Path)
	if err != nil {
		return nil, err
	}
	out := make([]ProcessSummary, 0, len(entries)/2)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil {
			continue
		}
		p, perr := r.Process(pid)
		if perr != nil {
			if errors.Is(perr, os.ErrNotExist) {
				continue
			}
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// Process reads /proc/[pid]/{status,cmdline} for a single PID.
func (r Root) Process(pid int) (ProcessSummary, error) {
	p := ProcessSummary{PID: pid}
	status, err := os.ReadFile(r.file(strconv.Itoa(pid), "status"))
	if err != nil {
		return p, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Name":
			p.Comm = val
		case "State":
			p.State = val
		case "PPid":
			p.PPID, _ = strconv.Atoi(val)
		case "Uid":
			f := strings.Fields(val)
			if len(f) > 0 {
				p.UID, _ = strconv.Atoi(f[0])
			}
		case "Gid":
			f := strings.Fields(val)
			if len(f) > 0 {
				p.GID, _ = strconv.Atoi(f[0])
			}
		case "Threads":
			p.Threads, _ = strconv.Atoi(val)
		case "VmSize":
			p.VMSize = parseKBytes(val)
		case "VmRSS":
			p.VMRSS = parseKBytes(val)
		}
	}
	cmd, err := os.ReadFile(r.file(strconv.Itoa(pid), "cmdline"))
	if err == nil {
		raw := strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00")
		p.CmdLine = raw
	}
	return p, nil
}

func parseKBytes(s string) uint64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.ParseUint(fields[0], 10, 64)
	if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
		n *= 1024
	}
	return n
}

// KernelInfo summarises /proc/version, /proc/cmdline and selected sysctls.
type KernelInfo struct {
	Version string
	CmdLine string
	Sysctl  map[string]string
}

// KernelInfo reads the kernel description and selected /proc/sys/kernel/* keys.
func (r Root) KernelInfo() (KernelInfo, error) {
	ki := KernelInfo{Sysctl: map[string]string{}}
	if b, err := os.ReadFile(r.file("version")); err == nil {
		ki.Version = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(r.file("cmdline")); err == nil {
		ki.CmdLine = strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
	}
	for _, key := range []string{"osrelease", "ostype", "hostname", "domainname", "version", "tainted"} {
		b, err := os.ReadFile(r.file("sys", "kernel", key))
		if err != nil {
			continue
		}
		ki.Sysctl[key] = strings.TrimSpace(string(b))
	}
	return ki, nil
}
