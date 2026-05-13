# FleetMind Integration Tests

The `e2e/` package contains integration tests that exercise the full FleetMind
server stack in-process on a real Linux host.

## What is tested

| Layer | How it is exercised |
|-------|---------------------|
| HTTP transport | Raw `net/http` calls to `/healthz` and `/mcp` |
| Bearer auth | Missing, wrong, and valid tokens |
| MCP handshake | SDK client `initialize` via Streamable HTTP |
| Tool registry | `tools/list` asserts all 20 tools are present |
| Tool calls | `system_info`, `cpu_info`, `memory_info`, `list_processes`, `get_process`, `list_mounts`, `list_network_interfaces`, `kernel_info`, `list_block_devices`, `list_pci_devices`, `list_usb_devices`, `list_dmi`, `list_sensors` |
| procfs/sysfs parsers | Real `/proc` and `/sys` data on the host/container |

## Running locally

### Direct (fastest)

The tests are standard Go tests with a `linux` build tag:

```sh
go test -v -timeout=120s ./e2e/...
```

This starts an in-process FleetMind server on an ephemeral port, runs through
the full MCP client/server handshake, and shuts down cleanly.

### Inside an LXD container (CI-like)

FleetMind is designed to run on real Linux hosts. Running the tests inside an
LXD system container gives a more realistic environment (clean process tree,
real mount table, systemd) than Docker, while still starting quickly.

The `e2e/e2e.sh` script automates the entire workflow:

```sh
# Install and initialise LXD (one-time)
sudo snap install lxd
sudo lxd init --auto

# Run the integration test suite inside an ephemeral container
./e2e/e2e.sh
```

What the script does:
1. Builds the test binary on the host (where Go is already installed).
2. Launches an ephemeral Ubuntu 24.04 system container.
3. Waits for systemd to become ready (with a 60-second timeout).
4. Pushes the test binary into the container and executes it.
5. Cleans up the container on exit (success or failure).

This avoids downloading Go inside the container and eliminates source-tree
path issues.

## Architecture

### Server lifecycle

```
┌─────────────────────────────────────┐
│  go test ./e2e                      │
│                                     │
│  ┌───────────────────────────────┐  │
│  │ net.Listen("tcp", "127.0.0.1:0") │  │
│  │ ↓                                │  │
│  │ mcpserver.New(Config{Listener})  │  │
│  │ ↓                                │  │
│  │ srv.Serve(ctx) in goroutine      │  │
│  │ ↓                                │  │
│  │ poll /healthz until 200          │  │
│  └───────────────────────────────┘  │
│                                     │
│  ┌───────────────────────────────┐  │
│  │ mcp.NewClient(...)              │  │
│  │ ↓                                │  │
│  │ StreamableClientTransport       │  │
│  │   + custom http.Client with     │  │
│  │   bearer token round-tripper    │  │
│  │ ↓                                │  │
│  │ client.Connect(ctx, transport)  │  │
│  │   → POST initialize             │  │
│  │   ← InitializeResult             │  │
│  └───────────────────────────────┘  │
│                                     │
│  session.ListTools(ctx)             │
│  session.CallTool(ctx, ...)         │
│                                     │
│  session.Close()                    │
│  cancel() → graceful shutdown       │
└─────────────────────────────────────┘
```

### Key design decisions

- **In-process**: The server runs in the same process as the test via `mcpserver.New`. This gives sub-second startup and avoids binary build overhead.
- **Ephemeral listener**: `net.Listen("tcp", "127.0.0.1:0")` guarantees no port conflicts between parallel test runs.
- **MCP SDK client**: We use `github.com/modelcontextprotocol/go-sdk/mcp` as the client to validate the exact transport, session, and structured-output paths that real consumers use.
- **Build tag**: `//go:build linux` skips these tests on macOS/Windows. They will fail fast with a helpful skip message.
- **DisableStandaloneSSE**: The test transport disables persistent SSE streams because the test suite is request/response only. This avoids maintaining a long-lived GET connection that complicates teardown.

## Why LXD (not Docker)

FleetMind reads authentic Linux host state from `/proc`, `/sys`, and kernel
interfaces. Docker application containers share the host kernel and present an
artificial process tree and mount namespace that makes many of FleetMind's
observability tools misleading:

- `/proc/cpuinfo` leaks the host CPU regardless of container limits.
- `/proc/self/mountinfo` shows overlayfs mounts, not the host filesystem.
- `ss` (`list_sockets`) sees the container's isolated network namespace.
- `journalctl` / `dmesg` need host journal access.
- Snapd (for confinement testing) does not run properly in Docker.

LXD **system containers** run a full init system (systemd), present a realistic
process tree, and support snapd natively. While FleetMind's integration tests do
not assert snap behaviour, running in a system container ensures the tools see
Linux state that is closer to what they would observe on a real deployment.

## CI

The `.github/workflows/ci.yml` runs both unit tests (native) and the e2e test
suite inside an ephemeral LXD container on every pull request.
