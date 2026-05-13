// Package snapconf is a thin wrapper around `snapctl get/set` for reading and
// writing snap configuration from inside a snap. Outside a snap (e.g. local
// development), the wrapper transparently falls back to environment variables.
package snapconf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNotInSnap is returned when snapctl is unavailable.
var ErrNotInSnap = errors.New("snapctl not available")

// InSnap reports whether the process is running inside a snap context.
func InSnap() bool {
	return os.Getenv("SNAP") != "" && os.Getenv("SNAP_NAME") != ""
}

// Get reads a configuration key. When running outside a snap, it falls back to
// the environment variable FLEETMIND_<UPPER(key)>.
func Get(ctx context.Context, key string) (string, error) {
	if !InSnap() {
		return os.Getenv(envKey(key)), nil
	}
	out, err := exec.CommandContext(ctx, "snapctl", "get", key).Output() //nolint:gosec // key is from a hardcoded allowlist
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("snapctl get %s: %w (stderr: %q)", key, err, exitErr.Stderr)
		}
		return "", fmt.Errorf("snapctl get %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Set writes a configuration key. Outside a snap this is a no-op.
func Set(ctx context.Context, key, value string) error {
	if !InSnap() {
		return nil
	}
	cmd := exec.CommandContext(ctx, "snapctl", "set", key+"="+value) //nolint:gosec // key/value from in-process logic
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("snapctl set %s: %w (output: %q)", key, err, out)
	}
	return nil
}

func envKey(key string) string {
	return "FLEETMIND_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}
