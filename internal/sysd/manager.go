// Package sysd is a small read-only wrapper around the systemd D-Bus API
// (org.freedesktop.systemd1). It exists because shelling out to
// `systemd-analyze` / `systemctl` fails under strict snap confinement — the
// helpers try to bind() an abstract Unix socket outside the snap's namespace
// for their own D-Bus client and AppArmor denies it. Talking to the
// well-known system bus socket (/run/dbus/system_bus_socket) is permitted by
// the base profile, so we go direct.
package sysd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/godbus/dbus/v5"
)

const (
	managerService = "org.freedesktop.systemd1"
	managerPath    = "/org/freedesktop/systemd1"
	managerIface   = "org.freedesktop.systemd1.Manager"
	unitIface      = "org.freedesktop.systemd1.Unit"
	timerIface     = "org.freedesktop.systemd1.Timer"
	propsIface     = "org.freedesktop.DBus.Properties"
)

// Manager is a session-bound handle to the systemd manager object. Use Close
// when finished; one Manager per tool invocation is fine — connection setup
// is sub-millisecond locally.
type Manager struct {
	conn *dbus.Conn
	mgr  dbus.BusObject
}

// Open returns a Manager bound to the system bus. Caller must Close.
func Open() (*Manager, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system bus: %w", err)
	}
	return &Manager{
		conn: conn,
		mgr:  conn.Object(managerService, dbus.ObjectPath(managerPath)),
	}, nil
}

// Close releases the underlying D-Bus connection.
func (m *Manager) Close() error {
	if m.conn == nil {
		return nil
	}
	return m.conn.Close()
}

// BootTimes is the raw monotonic-microsecond view of the boot sequence as
// systemd records it. Fields are 0 when the phase did not occur on this host
// (e.g. firmware/loader on a VM, initrd on a system booted without one).
type BootTimes struct {
	FirmwareMonotonicUsec  uint64 // microseconds BEFORE kernel boot
	LoaderMonotonicUsec    uint64 // microseconds BEFORE kernel boot
	InitRDMonotonicUsec    uint64 // microseconds AFTER kernel boot, when initrd started
	UserspaceMonotonicUsec uint64 // microseconds AFTER kernel boot, when userspace started
	FinishMonotonicUsec    uint64 // microseconds AFTER kernel boot, when default target became active
}

// BootTimes reads the relevant Manager properties in one trip.
func (m *Manager) BootTimes() (BootTimes, error) {
	var bt BootTimes
	if err := m.uintProperty("FirmwareTimestampMonotonic", &bt.FirmwareMonotonicUsec); err != nil {
		return bt, err
	}
	if err := m.uintProperty("LoaderTimestampMonotonic", &bt.LoaderMonotonicUsec); err != nil {
		return bt, err
	}
	if err := m.uintProperty("InitRDTimestampMonotonic", &bt.InitRDMonotonicUsec); err != nil {
		return bt, err
	}
	if err := m.uintProperty("UserspaceTimestampMonotonic", &bt.UserspaceMonotonicUsec); err != nil {
		return bt, err
	}
	if err := m.uintProperty("FinishTimestampMonotonic", &bt.FinishMonotonicUsec); err != nil {
		return bt, err
	}
	return bt, nil
}

// DefaultTarget returns the unit name of the configured default target
// (typically graphical.target or multi-user.target). Resolved by readlink
// rather than Manager.GetDefaultTarget — the latter is blocked by the
// snapd-generated AppArmor profile, which only whitelists a fixed subset of
// Manager methods.
func (m *Manager) DefaultTarget() (string, error) {
	for _, p := range []string{
		"/etc/systemd/system/default.target",
		"/usr/lib/systemd/system/default.target",
		"/lib/systemd/system/default.target",
	} {
		dst, err := os.Readlink(p)
		if err != nil {
			continue
		}
		return filepath.Base(dst), nil
	}
	return "default.target", nil
}

// UnitInfo mirrors the tuple Manager.ListUnits returns. Field order matches
// the D-Bus type signature `a(ssssssouso)` exactly — godbus uses field order
// to deserialize.
type UnitInfo struct {
	Name        string
	Description string
	LoadState   string
	ActiveState string
	SubState    string
	Follower    string
	Path        dbus.ObjectPath
	JobID       uint32
	JobType     string
	JobPath     dbus.ObjectPath
}

// ListUnits returns every currently-loaded unit.
func (m *Manager) ListUnits() ([]UnitInfo, error) {
	var units []UnitInfo
	if err := m.mgr.Call(managerIface+".ListUnits", 0).Store(&units); err != nil {
		return nil, fmt.Errorf("ListUnits: %w", err)
	}
	return units, nil
}

// UnitTimings is the per-unit boot-timing view used by blame and
// critical-chain. Zero values mean "no transition recorded this boot".
type UnitTimings struct {
	Name                      string
	InactiveExitMonotonicUsec uint64
	ActiveEnterMonotonicUsec  uint64
}

// StartupUsec is the unit's "blame" — the time it spent going from inactive
// to active during this boot. Returns 0 when either edge is missing.
func (t UnitTimings) StartupUsec() uint64 {
	if t.ActiveEnterMonotonicUsec == 0 || t.InactiveExitMonotonicUsec == 0 {
		return 0
	}
	if t.ActiveEnterMonotonicUsec <= t.InactiveExitMonotonicUsec {
		return 0
	}
	return t.ActiveEnterMonotonicUsec - t.InactiveExitMonotonicUsec
}

// UnitTimings reads the InactiveExit / ActiveEnter monotonic timestamps for
// a single unit path. Pass a path from ListUnits (avoids the LoadUnit cost).
func (m *Manager) UnitTimings(path dbus.ObjectPath, name string) (UnitTimings, error) {
	obj := m.conn.Object(managerService, path)
	out := UnitTimings{Name: name}
	if err := m.uintPropertyOn(obj, unitIface, "InactiveExitTimestampMonotonic", &out.InactiveExitMonotonicUsec); err != nil {
		return out, err
	}
	if err := m.uintPropertyOn(obj, unitIface, "ActiveEnterTimestampMonotonic", &out.ActiveEnterMonotonicUsec); err != nil {
		return out, err
	}
	return out, nil
}

// UnitAfter returns the After= dependencies of the named unit. Returns an
// empty slice if the unit doesn't exist or has no After= deps.
func (m *Manager) UnitAfter(path dbus.ObjectPath) ([]string, error) {
	obj := m.conn.Object(managerService, path)
	var v dbus.Variant
	if err := obj.Call(propsIface+".Get", 0, unitIface, "After").Store(&v); err != nil {
		return nil, fmt.Errorf("get After: %w", err)
	}
	var after []string
	if err := v.Store(&after); err != nil {
		return nil, fmt.Errorf("decode After variant: %w", err)
	}
	sort.Strings(after)
	return after, nil
}

// LoadUnit returns the object path for the given unit name, loading it into
// memory if it isn't already.
func (m *Manager) LoadUnit(name string) (dbus.ObjectPath, error) {
	var path dbus.ObjectPath
	if err := m.mgr.Call(managerIface+".LoadUnit", 0, name).Store(&path); err != nil {
		return "", fmt.Errorf("LoadUnit %s: %w", name, err)
	}
	return path, nil
}

// GetUnit returns the object path for an already-loaded unit. Unlike LoadUnit
// it will not load a unit on demand, but it is on the snapd AppArmor allowlist
// for Manager methods (LoadUnit is not). For units that became active during
// boot (targets, services in the critical chain), they remain loaded, so
// GetUnit is the right call.
func (m *Manager) GetUnit(name string) (dbus.ObjectPath, error) {
	var path dbus.ObjectPath
	if err := m.mgr.Call(managerIface+".GetUnit", 0, name).Store(&path); err != nil {
		return "", fmt.Errorf("GetUnit %s: %w", name, err)
	}
	return path, nil
}

// UnitPropertiesAll returns every property the unit publishes on the given
// interface as a map of D-Bus variants flattened to printable strings. We use
// this for unit_status's property bag.
func (m *Manager) UnitPropertiesAll(path dbus.ObjectPath, iface string) (map[string]string, error) {
	obj := m.conn.Object(managerService, path)
	raw := map[string]dbus.Variant{}
	if err := obj.Call(propsIface+".GetAll", 0, iface).Store(&raw); err != nil {
		return nil, fmt.Errorf("GetAll(%s): %w", iface, err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = formatVariant(v)
	}
	return out, nil
}

// TimerInfo bundles the timer-specific properties tools need.
type TimerInfo struct {
	NextElapseMonotonicUsec  uint64
	NextElapseRealtimeUsec   uint64
	LastTriggerUsec          uint64
	LastTriggerMonotonicUsec uint64
}

// TimerProperties reads the four scheduling properties off a *.timer unit.
func (m *Manager) TimerProperties(path dbus.ObjectPath) (TimerInfo, error) {
	obj := m.conn.Object(managerService, path)
	var info TimerInfo
	props := []struct {
		key string
		dst *uint64
	}{
		{"NextElapseUSecMonotonic", &info.NextElapseMonotonicUsec},
		{"NextElapseUSecRealtime", &info.NextElapseRealtimeUsec},
		{"LastTriggerUSec", &info.LastTriggerUsec},
		{"LastTriggerUSecMonotonic", &info.LastTriggerMonotonicUsec},
	}
	for _, p := range props {
		// Best-effort: a missing property on older systemd versions shouldn't
		// fail the whole call.
		_ = m.uintPropertyOn(obj, timerIface, p.key, p.dst)
	}
	return info, nil
}

// TriggersUnit reads the Triggers property (the service this timer activates).
func (m *Manager) TriggersUnit(path dbus.ObjectPath) (string, error) {
	obj := m.conn.Object(managerService, path)
	var v dbus.Variant
	if err := obj.Call(propsIface+".Get", 0, unitIface, "Triggers").Store(&v); err != nil {
		return "", fmt.Errorf("get Triggers: %w", err)
	}
	var triggers []string
	if err := v.Store(&triggers); err != nil {
		return "", fmt.Errorf("decode Triggers variant: %w", err)
	}
	if len(triggers) == 0 {
		return "", nil
	}
	return triggers[0], nil
}

// --- internal helpers --------------------------------------------------------

func (m *Manager) uintProperty(name string, dst *uint64) error {
	return m.uintPropertyOn(m.mgr, managerIface, name, dst)
}

func (m *Manager) uintPropertyOn(obj dbus.BusObject, iface, name string, dst *uint64) error {
	var v dbus.Variant
	if err := obj.Call(propsIface+".Get", 0, iface, name).Store(&v); err != nil {
		return fmt.Errorf("get %s.%s: %w", iface, name, err)
	}
	if err := v.Store(dst); err != nil {
		return fmt.Errorf("decode %s.%s variant: %w", iface, name, err)
	}
	return nil
}

// Format a Variant as a printable string. Numeric, string and []string types
// get clean output; everything else falls back to fmt.Sprint of the Value().
func formatVariant(v dbus.Variant) string {
	val := v.Value()
	switch t := val.(type) {
	case string:
		return t
	case []string:
		return joinStrings(t, " ")
	case bool, int8, int16, int32, int64, uint8, uint16, uint32, uint64, float64:
		return fmt.Sprint(t)
	case dbus.ObjectPath:
		return string(t)
	default:
		return fmt.Sprint(val)
	}
}

func joinStrings(xs []string, sep string) string {
	if len(xs) == 0 {
		return ""
	}
	out := xs[0]
	for _, s := range xs[1:] {
		out += sep + s
	}
	return out
}
