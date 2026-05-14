# FleetMind — agent guide

Read-only Linux observability MCP server, packaged as a strictly-confined snap.
The security model is the codebase: **nothing here may ever mutate host state.**

## Commands

```sh
make build              # → bin/fleetmind
make test               # go test -race ./...
make lint               # golangci-lint v2 (config in .golangci.yml)
make fmt                # gofumpt + goimports
make snap               # snapcraft pack (slow; only when packaging)
```

For a local smoke test without the snap:

```sh
FLEETMIND_TOKEN=devtoken ./bin/fleetmind --port 18765 &
curl -sS -H 'Authorization: Bearer devtoken' http://127.0.0.1:18765/healthz
```

## Toolchain gotchas

- **Go 1.25+** required (MCP SDK constraint). The system `go` may be older;
  prefix builds with `GOTOOLCHAIN=auto` or `GOTOOLCHAIN=go1.25.10` so it
  auto-fetches.
- **`golangci-lint` v2** required. v1 chokes on Go 1.25 modules. Install with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`.
  The Makefile assumes it's on `PATH` (or use `~/go/bin/golangci-lint`).

## Architecture rules

- **Snap plugs are the safety boundary.** Only `*-observe`, `network-bind`,
  and read-only host-fs plugs (`system-backup`). Never add a `*-control` plug,
  `home`, or `system-files` write rules.
- **All `os/exec` goes through `internal/exectool`.** Fixed argv, 10s timeout,
  4 MiB stdout cap, `LC_ALL=C`, hardcoded `PATH`. Do not call `exec` directly
  from tools.
- **User-supplied arguments that become CLI flags must be validated against a
  regex/allowlist before exectool sees them.** See `internal/tools/logs.go`
  for the journal-unit and priority patterns.
- **`internal/procfs` and `internal/sysfs` parse `/proc` and `/sys`.** Both
  accept a `NewRoot(path)` for tests — never use absolute paths directly.

## Adding a tool

1. Create `internal/tools/<concern>.go`.
2. Define typed `<tool>In` / `<tool>Out` structs.
3. Implement `register<Name>(s *mcp.Server, d Deps)` using `mcp.AddTool`.
4. Add a line to `RegisterAll` in `internal/tools/tools.go`.
5. Use `textResult(...)` for the human-readable summary.

### Output-schema landmines

The MCP SDK generates JSON Schema from Go types, and **Claude Code's
client-side validator is stricter than the server's**:

- **`any` produces `"propertyName": true`** (the always-valid boolean schema)
  which Claude Code rejects. Use `map[string]any` for free-form objects or
  define a concrete struct.
- **`json.RawMessage` produces a string schema** but round-trips as JSON,
  causing validation failures on the response. Unmarshal into `map[string]any`
  instead — see `internal/tools/block.go`.

## Secrets handling

- The bearer token is set/read via `internal/snapconf` (snapctl) and mirrored
  to `$SNAP_COMMON/token` at mode `0600`. **Never log the token value.**
- Tests must not write the token to stdout/stderr either.

## Layout

```
cmd/fleetmind            entrypoint, flag parsing, signal handling
internal/mcpserver       *mcp.Server wiring, bearer auth, token bootstrap
internal/tools           one file per tool + RegisterAll
internal/exectool        the only path to os/exec
internal/procfs          /proc parsers
internal/sysfs           /sys parsers
internal/snapconf        snapctl get/set wrapper (env fallback outside snap)
snap/                    snapcraft.yaml + install/configure hooks
```
