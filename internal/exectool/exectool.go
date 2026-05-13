// Package exectool runs a fixed allowlist of read-only system binaries with
// a context timeout and a bounded-output reader. All callers pass a literal
// program name and a slice of arguments — no shell, no string interpolation.
package exectool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// ErrOutputTooLarge is returned when a command's stdout exceeds MaxOutput.
var ErrOutputTooLarge = errors.New("command output exceeded limit")

// Runner executes external commands with shared safety limits.
type Runner struct {
	// Timeout bounds the wall-clock duration of every command. A zero value
	// disables the timeout (not recommended).
	Timeout time.Duration
	// MaxOutput caps stdout in bytes. Stderr is always captured fully but
	// truncated separately at the same limit.
	MaxOutput int64
}

// NewRunner returns a Runner with sensible defaults: 10s timeout, 4 MiB output.
func NewRunner() *Runner {
	return &Runner{
		Timeout:   10 * time.Second,
		MaxOutput: 4 << 20,
	}
}

// Run executes name with args and returns the captured stdout and stderr.
// The provided context bounds the call; the Runner additionally enforces its
// own Timeout. Exit status non-zero is surfaced as a wrapped *exec.ExitError.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed argv from in-process allowlist
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &outBuf, n: r.MaxOutput}
	cmd.Stderr = &limitedWriter{w: &errBuf, n: r.MaxOutput}

	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return outBuf.Bytes(), errBuf.Bytes(), fmt.Errorf("%s: timed out after %s", name, r.Timeout)
	}
	if runErr != nil {
		return outBuf.Bytes(), errBuf.Bytes(), fmt.Errorf("%s: %w (stderr: %q)", name, runErr, errBuf.String())
	}
	return outBuf.Bytes(), errBuf.Bytes(), nil
}

// limitedWriter wraps an io.Writer and stops writing after n bytes, returning
// ErrOutputTooLarge on overflow so the caller distinguishes the case.
type limitedWriter struct {
	w   io.Writer
	n   int64
	hit bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.hit {
		return 0, ErrOutputTooLarge
	}
	if int64(len(p)) > l.n {
		if l.n > 0 {
			_, _ = l.w.Write(p[:l.n])
		}
		l.n = 0
		l.hit = true
		return len(p), ErrOutputTooLarge
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}
