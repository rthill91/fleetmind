// Command fleetmind serves an MCP read-only view of the local Linux system
// over Streamable HTTP. See the project README for the full tool catalogue
// and the snap installation instructions.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
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
		bind    = flag.String("bind", "127.0.0.1", "host/IP to bind (default loopback)")
		port    = flag.Int("port", 0, "listen port (0 = read from snap config or default 8765)")
		verbose = flag.Bool("verbose", false, "enable debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	resolvedPort, err := resolvePort(ctx, *port)
	if err != nil {
		return err
	}

	token, err := mcpserver.EnsureToken(ctx, logger)
	if err != nil {
		return fmt.Errorf("token bootstrap: %w", err)
	}

	srv := mcpserver.New(mcpserver.Config{
		BindHost: *bind,
		Port:     resolvedPort,
		Token:    token,
		Version:  buildVersion(),
		Logger:   logger,
	})

	return srv.Serve(ctx)
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

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "0.0.0+devel"
}
