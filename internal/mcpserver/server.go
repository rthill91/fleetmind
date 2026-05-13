// Package mcpserver assembles the FleetMind MCP server: token bootstrap,
// bearer-token middleware and tool registration over a Streamable HTTP
// transport. Optional fleet mode (peer discovery + heartbeat SSE) is wired
// onto the same listener.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gjolly/fleetmind/internal/exectool"
	"github.com/gjolly/fleetmind/internal/fleet"
	"github.com/gjolly/fleetmind/internal/procfs"
	"github.com/gjolly/fleetmind/internal/sysfs"
	"github.com/gjolly/fleetmind/internal/tools"
	"github.com/gjolly/fleetmind/internal/webui"
)

// Config controls how the server binds and authenticates.
type Config struct {
	BindHost string
	Port     int
	// Listener, if set, overrides BindHost:Port. Used by tests that need an
	// ephemeral port.
	Listener net.Listener
	Token    string
	Version  string
	Logger   *slog.Logger

	// Fleet, when non-nil, enables fleet mode: the server hosts /fleet/* and
	// the Manager joins/maintains the peer mesh while Serve is running.
	Fleet *FleetConfig
}

// FleetConfig configures optional fleet membership. BootstrapURL may be empty
// to start a solo fleet. AdvertiseURL is the URL other peers will dial back to
// reach this node and must be reachable from them.
type FleetConfig struct {
	BootstrapURL string
	AdvertiseURL string
}

// Server bundles the MCP server, its HTTP listener and (optionally) a fleet
// manager.
type Server struct {
	cfg      Config
	mcp      *mcp.Server
	http     *http.Server
	logger   *slog.Logger
	fleetReg *fleet.Registry
	fleetMgr *fleet.Manager
}

// New builds an MCP server with all FleetMind tools registered.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	mcpSrv := mcp.NewServer(&mcp.Implementation{
		Name:    "fleetmind",
		Version: cfg.Version,
	}, &mcp.ServerOptions{
		Instructions: "Read-only Linux system observability. Tools cover hardware, " +
			"kernel, processes, mounts, network, sensors and journal logs. Nothing here " +
			"can mutate the host: the snap is strictly confined to *-observe interfaces. " +
			"In fleet mode, list_fleet reports every peer MCP server in the mesh.",
	})

	var (
		fleetReg *fleet.Registry
		fleetMgr *fleet.Manager
	)
	if cfg.Fleet != nil {
		reg, mgr, err := buildFleet(*cfg.Fleet, cfg.Token, cfg.Version, cfg.Logger)
		if err != nil {
			return nil, fmt.Errorf("fleet init: %w", err)
		}
		fleetReg, fleetMgr = reg, mgr
	}

	deps := tools.Deps{
		Exec:   exectool.NewRunner(),
		ProcFS: procfs.Default,
		SysFS:  sysfs.Default,
		Logger: cfg.Logger,
		Fleet:  fleetReg,
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
	if fleetReg != nil {
		mux.Handle("/fleet/", bearerAuth(fleet.Handler(fleetReg), cfg.Token, cfg.Logger))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// Static operator console. The HTML/JS/CSS load without auth so the user
	// can paste their token; every API call the SPA makes carries that token
	// against the existing bearer-protected endpoints.
	mux.Handle("/ui/", webui.Handler())
	mux.Handle("/ui", webui.Handler())
	// Some MCP clients probe OAuth discovery endpoints. We don't speak OAuth
	// (bearer-only), so return a JSON 404 they can parse rather than the
	// default text/plain "404 page not found".
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}` + "\n"))
	})

	addr := net.JoinHostPort(cfg.BindHost, fmt.Sprintf("%d", cfg.Port))
	if cfg.Listener != nil {
		addr = cfg.Listener.Addr().String()
	}
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // streaming responses can be long-lived
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
	}

	return &Server{
		cfg:      cfg,
		mcp:      mcpSrv,
		http:     httpSrv,
		logger:   cfg.Logger,
		fleetReg: fleetReg,
		fleetMgr: fleetMgr,
	}, nil
}

func buildFleet(fc FleetConfig, token, version string, log *slog.Logger) (*fleet.Registry, *fleet.Manager, error) {
	if fc.AdvertiseURL == "" {
		return nil, nil, errors.New("advertise URL is required in fleet mode")
	}
	nodeID, err := fleet.NewNodeID()
	if err != nil {
		return nil, nil, fmt.Errorf("generate node id: %w", err)
	}
	now := time.Now().UTC()
	self := fleet.Peer{
		NodeID:        nodeID,
		AdvertiseURL:  fc.AdvertiseURL,
		Version:       version,
		Tools:         append([]string(nil), tools.AllToolNames...),
		JoinedAt:      now,
		LastHeartbeat: now,
	}
	reg := fleet.NewRegistry(self, log)
	mgr := fleet.NewManager(reg, fleet.ManagerOptions{
		BootstrapURL: fc.BootstrapURL,
		Token:        token,
	}, log)
	return reg, mgr, nil
}

// Addr returns the resolved listen address. Only meaningful after Serve has
// been called (or if a Listener was provided in Config).
func (s *Server) Addr() string { return s.http.Addr }

// Serve binds the listener and blocks until ctx is cancelled. The HTTP server
// is shut down gracefully with a 5-second grace period. When fleet mode is
// enabled, the manager runs for the full Serve lifetime.
func (s *Server) Serve(ctx context.Context) error {
	listenErr := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", s.http.Addr, "endpoint", "/mcp",
			"fleet", s.fleetMgr != nil)
		var err error
		if s.cfg.Listener != nil {
			err = s.http.Serve(s.cfg.Listener)
		} else {
			err = s.http.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
			return
		}
		listenErr <- nil
	}()

	var (
		mgrCtx    context.Context
		mgrCancel context.CancelFunc
		mgrWg     sync.WaitGroup
	)
	if s.fleetMgr != nil {
		mgrCtx, mgrCancel = context.WithCancel(ctx)
		mgrWg.Add(1)
		go func() {
			defer mgrWg.Done()
			s.fleetMgr.Run(mgrCtx)
		}()
	}

	select {
	case <-ctx.Done():
		// Deliberately decouple shutdown from the cancelled parent ctx so we
		// still get the full grace period to drain in-flight requests.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // fresh ctx is intentional for graceful drain
			if mgrCancel != nil {
				mgrCancel()
				mgrWg.Wait()
			}
			return fmt.Errorf("http shutdown: %w", err)
		}
		if mgrCancel != nil {
			mgrCancel()
			mgrWg.Wait()
		}
		return nil
	case err := <-listenErr:
		if mgrCancel != nil {
			mgrCancel()
			mgrWg.Wait()
		}
		return err
	}
}
