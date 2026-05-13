package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// unitRE matches the systemd unit-name pattern we accept. Anything else is
// refused before reaching journalctl.
var unitRE = regexp.MustCompile(`^[A-Za-z0-9@:_.\-]+$`)

type readJournalIn struct {
	Unit     string `json:"unit,omitempty" jsonschema:"limit to a specific systemd unit (optional)"`
	Lines    int    `json:"lines,omitempty" jsonschema:"how many recent lines to return (default 100, max 5000)"`
	Priority string `json:"priority,omitempty" jsonschema:"minimum priority: emerg|alert|crit|err|warning|notice|info|debug"`
	Since    string `json:"since,omitempty" jsonschema:"timestamp accepted by --since (e.g. '1h ago', '2026-05-13 09:00')"`
	Boot     int    `json:"boot,omitempty" jsonschema:"boot offset for journalctl -b (0 = current boot, -1 = previous boot, …). Valid range -10..0."`
	Match    string `json:"match,omitempty" jsonschema:"PCRE regex passed to journalctl --grep (max 200 chars)"`
}

type readJournalOut struct {
	Raw string `json:"raw"`
}

type readDmesgIn struct {
	Lines int `json:"lines,omitempty" jsonschema:"how many recent lines to return (default 200, max 5000)"`
}

type readDmesgOut struct {
	Raw string `json:"raw"`
}

func registerLogs(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "read_journal",
		Description: "Recent journald entries via `journalctl -o short-iso`. " +
			"Requires the log-observe interface to be connected (`snap connect fleetmind:log-observe`).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readJournalIn) (*mcp.CallToolResult, readJournalOut, error) {
		args, err := journalArgs(in)
		if err != nil {
			return nil, readJournalOut{}, err
		}
		stdout, _, err := d.Exec.Run(ctx, "journalctl", args...)
		if err != nil {
			return nil, readJournalOut{}, fmt.Errorf("journalctl: %w", err)
		}
		return textResult("journalctl returned %d bytes", len(stdout)), readJournalOut{Raw: string(stdout)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "read_dmesg",
		Description: "Recent kernel ring-buffer lines via `dmesg --time-format=iso`. " +
			"Requires the log-observe interface.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readDmesgIn) (*mcp.CallToolResult, readDmesgOut, error) {
		lines := in.Lines
		if lines <= 0 {
			lines = 200
		}
		if lines > 5000 {
			return nil, readDmesgOut{}, errors.New("lines must be <= 5000")
		}
		// dmesg is read-only-no-clear by default. We deliberately do NOT pass
		// --read-clear (which is the consume-and-clear flag, has no boolean
		// argument, and would also require an extra capability).
		stdout, _, err := d.Exec.Run(ctx, "dmesg", "--time-format=iso", "--color=never")
		if err != nil {
			return nil, readDmesgOut{}, fmt.Errorf("dmesg: %w", err)
		}
		out := tailLines(string(stdout), lines)
		return textResult("dmesg: %d bytes after tail", len(out)), readDmesgOut{Raw: out}, nil
	})
}

func journalArgs(in readJournalIn) ([]string, error) {
	args := []string{"--no-pager", "-o", "short-iso"}
	lines := in.Lines
	if lines <= 0 {
		lines = 100
	}
	if lines > 5000 {
		return nil, errors.New("lines must be <= 5000")
	}
	args = append(args, "-n", strconv.Itoa(lines))

	if in.Unit != "" {
		if !unitRE.MatchString(in.Unit) {
			return nil, fmt.Errorf("invalid unit name %q", in.Unit)
		}
		args = append(args, "--unit", in.Unit)
	}
	if in.Priority != "" {
		allowed := map[string]bool{
			"emerg": true, "alert": true, "crit": true, "err": true,
			"warning": true, "notice": true, "info": true, "debug": true,
		}
		if !allowed[in.Priority] {
			return nil, fmt.Errorf("invalid priority %q", in.Priority)
		}
		args = append(args, "-p", in.Priority)
	}
	if in.Since != "" {
		if !sinceRE.MatchString(in.Since) {
			return nil, fmt.Errorf("invalid --since value %q", in.Since)
		}
		args = append(args, "--since", in.Since)
	}
	if in.Boot != 0 {
		if in.Boot < -10 || in.Boot > 0 {
			return nil, fmt.Errorf("boot must be in [-10, 0], got %d", in.Boot)
		}
		args = append(args, "-b", strconv.Itoa(in.Boot))
	}
	if in.Match != "" {
		if len(in.Match) > 200 {
			return nil, fmt.Errorf("match regex too long (%d > 200)", len(in.Match))
		}
		if !matchRE.MatchString(in.Match) {
			return nil, fmt.Errorf("invalid match regex %q (control chars or non-printable bytes rejected)", in.Match)
		}
		if _, err := regexp.Compile(in.Match); err != nil {
			return nil, fmt.Errorf("match regex does not compile: %w", err)
		}
		args = append(args, "--grep", in.Match)
	}
	return args, nil
}

// matchRE is a safety screen for --grep input: printable ASCII only. We still
// compile the regex in Go to reject patterns journalctl would also refuse.
var matchRE = regexp.MustCompile(`^[\x20-\x7E]+$`)

// sinceRE accepts ASCII timestamps and the common human-readable forms
// journalctl supports. Anything more exotic is rejected to keep argv tight.
var sinceRE = regexp.MustCompile(`^[A-Za-z0-9:\-+ ]{1,40}$`)

func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	// Walk from the end counting '\n'.
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count > n {
				return s[i+1:]
			}
		}
	}
	return s
}
