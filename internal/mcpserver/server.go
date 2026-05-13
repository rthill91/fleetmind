// Package mcpserver assembles the FleetMind MCP server: token bootstrap,
// bearer-token middleware and tool registration over a Streamable HTTP
// transport.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/exectool"
	"github.com/gjolly/fleetmind/internal/procfs"
	"github.com/gjolly/fleetmind/internal/sysfs"
	"github.com/gjolly/fleetmind/internal/tools"
)

// Config controls how the server binds and authenticates.
type Config struct {
	BindHost string
	Port     int
	Token    string
	Version  string
	Logger   *slog.Logger
}

// Server bundles the MCP server and its HTTP listener.
type Server struct {
	cfg    Config
	mcp    *mcp.Server
	http   *http.Server
	logger *slog.Logger
}

// New builds an MCP server with all FleetMind tools registered.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	mcpSrv := mcp.NewServer(&mcp.Implementation{
		Name:    "fleetmind",
		Version: cfg.Version,
	}, &mcp.ServerOptions{
		Instructions: "Read-only Linux system observability. Tools cover hardware, " +
			"kernel, processes, mounts, network, sensors and journal logs. Nothing here " +
			"can mutate the host: the snap is strictly confined to *-observe interfaces.",
	})

	deps := tools.Deps{
		Exec:   exectool.NewRunner(),
		ProcFS: procfs.Default,
		SysFS:  sysfs.Default,
		Logger: cfg.Logger,
	}
	tools.RegisterAll(mcpSrv, deps)

	mux := http.NewServeMux()
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpSrv },
		&mcp.StreamableHTTPOptions{
			Stateless:      false,
			JSONResponse:   false,
			Logger:         cfg.Logger,
			SessionTimeout: 5 * time.Minute,
		},
	)
	mux.Handle("/mcp", bearerAuth(streamable, cfg.Token, cfg.Logger))
	mux.Handle("/mcp/", bearerAuth(streamable, cfg.Token, cfg.Logger))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// Some MCP clients probe OAuth discovery endpoints. We don't speak OAuth
	// (bearer-only), so return a JSON 404 they can parse rather than the
	// default text/plain "404 page not found".
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}` + "\n"))
	})

	addr := net.JoinHostPort(cfg.BindHost, fmt.Sprintf("%d", cfg.Port))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // streaming responses can be long-lived
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
	}

	return &Server{cfg: cfg, mcp: mcpSrv, http: httpSrv, logger: cfg.Logger}
}

// Serve binds the listener and blocks until ctx is cancelled. The HTTP server
// is shut down gracefully with a 5-second grace period.
func (s *Server) Serve(ctx context.Context) error {
	listenErr := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", s.http.Addr, "endpoint", "/mcp")
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
			return
		}
		listenErr <- nil
	}()

	select {
	case <-ctx.Done():
		// Deliberately decouple shutdown from the cancelled parent ctx so we
		// still get the full grace period to drain in-flight requests.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // fresh ctx is intentional for graceful drain
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	case err := <-listenErr:
		return err
	}
}
