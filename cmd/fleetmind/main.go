// Command fleetmind serves an MCP read-only view of the local Linux system
// over Streamable HTTP. See the project README for the full tool catalogue
// and the snap installation instructions.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/gjolly/fleetmind/internal/mcpserver"
	"github.com/gjolly/fleetmind/internal/snapconf"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fleetmind:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		bind         = flag.String("bind", "127.0.0.1", "host/IP to bind (default loopback)")
		port         = flag.Int("port", 0, "listen port (0 = read from snap config or default 8765)")
		verbose      = flag.Bool("verbose", false, "enable debug logging")
		fleetMode    = flag.Bool("fleet", false, "enable fleet mode (mount /fleet/* and run the peer manager)")
		joinURL      = flag.String("join-url", "", "URL of an existing fleet node to bootstrap from (empty = solo fleet)")
		advertiseURL = flag.String("advertise-url", "", "URL other peers should use to reach this node (defaults to http://<bind>:<port>)")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	resolvedBind, err := resolveBind(ctx, *bind)
	if err != nil {
		return err
	}

	resolvedPort, err := resolvePort(ctx, *port)
	if err != nil {
		return err
	}

	token, err := mcpserver.EnsureToken(ctx, logger)
	if err != nil {
		return fmt.Errorf("token bootstrap: %w", err)
	}

	fleetCfg, err := resolveFleet(ctx, *fleetMode, *joinURL, *advertiseURL, resolvedBind, resolvedPort)
	if err != nil {
		return err
	}

	srv, err := mcpserver.New(mcpserver.Config{
		BindHost: resolvedBind,
		Port:     resolvedPort,
		Token:    token,
		Version:  buildVersion(),
		Logger:   logger,
		Fleet:    fleetCfg,
	})
	if err != nil {
		return err
	}

	return srv.Serve(ctx)
}

// resolveBind honours, in order: the --bind flag if non-empty, then the snap
// config key "bind", then the default 127.0.0.1.
func resolveBind(ctx context.Context, flagBind string) (string, error) {
	if flagBind != "127.0.0.1" {
		return flagBind, nil
	}
	raw, err := snapconf.Get(ctx, "bind")
	if err != nil {
		return "", fmt.Errorf("read snap config bind: %w", err)
	}
	if raw == "" {
		return "127.0.0.1", nil
	}
	return raw, nil
}

// resolvePort honours, in order: the --port flag if non-zero, then the snap
// config key "port", then the default 8765.
func resolvePort(ctx context.Context, flagPort int) (int, error) {
	if flagPort > 0 {
		return flagPort, nil
	}
	raw, err := snapconf.Get(ctx, "port")
	if err != nil {
		return 0, fmt.Errorf("read snap config port: %w", err)
	}
	if raw == "" {
		return 8765, nil
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid snap config port %q", raw)
	}
	return p, nil
}

// resolveFleet decides whether to enable fleet mode and builds the FleetConfig.
// Activation: --fleet flag OR a non-empty snap config key "fleet" (any of
// "1"/"true"/"yes"/"on") OR a non-empty join-url/advertise-url anywhere.
// Returns nil (fleet disabled) when no signal is set.
func resolveFleet(ctx context.Context, flagOn bool, flagJoin, flagAdvertise, bind string, port int) (*mcpserver.FleetConfig, error) {
	join := flagJoin
	if join == "" {
		v, err := snapconf.Get(ctx, "join-url")
		if err != nil {
			return nil, fmt.Errorf("read snap config join-url: %w", err)
		}
		join = strings.TrimSpace(v)
	}
	advertise := flagAdvertise
	if advertise == "" {
		v, err := snapconf.Get(ctx, "advertise-url")
		if err != nil {
			return nil, fmt.Errorf("read snap config advertise-url: %w", err)
		}
		advertise = strings.TrimSpace(v)
	}

	enabled := flagOn || join != "" || advertise != ""
	if !enabled {
		raw, err := snapconf.Get(ctx, "fleet")
		if err != nil {
			return nil, fmt.Errorf("read snap config fleet: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "1", "true", "yes", "on":
			enabled = true
		}
	}
	if !enabled {
		return nil, nil
	}

	if advertise == "" {
		host := bind
		if host == "0.0.0.0" || host == "::" {
			return nil, fmt.Errorf("--advertise-url is required when bind is %q (peers cannot dial a wildcard address)", host)
		}
		advertise = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	}
	if err := validateURL(advertise, "advertise-url"); err != nil {
		return nil, err
	}
	if join != "" {
		if err := validateURL(join, "join-url"); err != nil {
			return nil, err
		}
	}
	return &mcpserver.FleetConfig{BootstrapURL: join, AdvertiseURL: advertise}, nil
}

func validateURL(raw, label string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", label, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid %s %q: scheme must be http or https", label, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid %s %q: missing host", label, raw)
	}
	return nil
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "0.0.0+devel"
}
