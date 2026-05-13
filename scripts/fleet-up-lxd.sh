#!/bin/bash
# fleet-up-lxd.sh — Bring up a two-node FleetMind fleet on local LXD VMs.
#
# Workflow:
#   1. Build the snap with `make snap`.
#   2. Launch two Ubuntu LXD VMs (defaults: fleetmind-a, fleetmind-b).
#   3. Push and install the snap in each VM.
#   4. Connect the non-auto-connect observe plugs.
#   5. Configure VM-A as the fleet seed and VM-B as a joiner, sharing one
#      bearer token (the shared fleet secret).
#   6. Wait for the mesh to converge to two members and print the
#      Claude Code wiring snippet for VM-A.
#
# Prerequisites:
#   - lxc (LXD), snapcraft, jq, curl, openssl on PATH.
#   - A working LXD bridge (lxdbr0 by default).
#   - The `bind` snap-config key must be honoured by the daemon. This is
#     introduced by https://github.com/rthill91/fleetmind/pull/2 — without
#     that PR, VM-B cannot reach VM-A on the bridge IP and the script will
#     hang at the healthz poll for VM-A.
#
# Usage: scripts/fleet-up-lxd.sh [flags]
#   --name-a NAME   seed VM name        (default: fleetmind-a)
#   --name-b NAME   joiner VM name      (default: fleetmind-b)
#   --image  NAME   LXD image           (default: ubuntu:24.04)
#   --port   PORT   listen port in VMs  (default: 8765)
#   --clean         stop+delete existing VMs of the same name before launching
#   -h, --help      show help
set -euo pipefail

NAME_A="fleetmind-a"
NAME_B="fleetmind-b"
IMAGE="ubuntu:24.04"
PORT="8765"
CLEAN=0

usage() { sed -n '2,28p' "$0"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --name-a) NAME_A="$2"; shift 2 ;;
    --name-b) NAME_B="$2"; shift 2 ;;
    --image)  IMAGE="$2";  shift 2 ;;
    --port)   PORT="$2";   shift 2 ;;
    --clean)  CLEAN=1;     shift   ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

log() { printf '[fleet-up] %s\n' "$*" >&2; }
die() { printf '[fleet-up] error: %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"
}
need lxc
need snapcraft
need jq
need curl
need openssl

# Run from the repo root so `make snap` finds snapcraft.yaml.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"
[ -f snap/snapcraft.yaml ] || die "expected snap/snapcraft.yaml at $REPO_ROOT"

# ---- 1. Build the snap ------------------------------------------------------
# snapcraft's craft-cli output library crashes silently (exit 120, zero output)
# when stdout is a regular file rather than a TTY or pipe. Piping through tee
# keeps stdout a pipe so snapcraft behaves, while still capturing the log.
log "Building snap with 'make snap' (this may take a few minutes)…"
SNAP_BUILD_LOG="$(mktemp -t fleet-up-snap-build.XXXXXX.log)"
set +e
make snap 2>&1 | tee "$SNAP_BUILD_LOG" >/dev/null
SNAP_RC=${PIPESTATUS[0]}
set -e
if [ "$SNAP_RC" -ne 0 ]; then
  echo >&2
  echo "[fleet-up] 'make snap' failed — last 40 lines of output:" >&2
  tail -40 "$SNAP_BUILD_LOG" >&2
  echo "[fleet-up] full log: $SNAP_BUILD_LOG" >&2
  exit 1
fi
rm -f "$SNAP_BUILD_LOG"

SNAP_FILE="$(ls -1t fleetmind_*.snap 2>/dev/null | head -1)"
[ -n "$SNAP_FILE" ] || die "make snap did not produce a fleetmind_*.snap file"
log "Built $SNAP_FILE"

# ---- 2. Shared bearer token -------------------------------------------------
TOKEN="$(openssl rand -hex 32)"

# ---- helpers ----------------------------------------------------------------
vm_exists() { lxc info "$1" >/dev/null 2>&1; }

ensure_vm() {
  local name="$1"
  if vm_exists "$name"; then
    if [ "$CLEAN" -eq 1 ]; then
      log "Removing existing VM $name (--clean)…"
      lxc stop "$name" --force >/dev/null 2>&1 || true
      lxc delete "$name" >/dev/null
    else
      die "VM $name already exists. Re-run with --clean to recreate it (or use scripts/fleet-down-lxd.sh)."
    fi
  fi
  log "Launching VM $name from $IMAGE…"
  lxc launch "$IMAGE" "$name" --vm \
    -c limits.cpu=2 -c limits.memory=2GiB >/dev/null
}

wait_ready() {
  local name="$1"
  log "Waiting for $name LXD agent to come up…"
  # `lxc launch --vm` returns before the in-guest agent is reachable; poll until
  # `lxc exec` actually works, then run cloud-init / systemd checks via exec.
  local deadline=$(( $(date +%s) + 180 ))
  until lxc exec "$name" -- true >/dev/null 2>&1; do
    [ "$(date +%s)" -lt "$deadline" ] \
      || die "$name LXD agent did not become reachable within 180s"
    sleep 2
  done
  log "Waiting for $name to finish booting (cloud-init + systemd)…"
  if ! lxc exec "$name" -- bash -c 'command -v cloud-init >/dev/null && cloud-init status --wait' >/dev/null 2>&1; then
    lxc exec "$name" -- bash -c \
      'timeout 180 bash -c "until systemctl is-system-running --quiet || systemctl is-system-running 2>&1 | grep -q degraded; do sleep 2; done"' \
      || die "$name did not finish booting within 180s"
  fi
}

install_snap() {
  local name="$1"
  log "Pushing $SNAP_FILE into $name…"
  lxc file push "$SNAP_FILE" "$name/root/$SNAP_FILE"
  log "Installing snap in $name…"
  lxc exec "$name" -- snap install --dangerous "/root/$SNAP_FILE" >/dev/null
  log "Connecting observe plugs in $name…"
  lxc exec "$name" -- snap connect fleetmind:log-observe
  lxc exec "$name" -- snap connect fleetmind:kernel-module-observe
  log "Seeding snap config in $name (token, port, bind)…"
  lxc exec "$name" -- snap set fleetmind \
    token="$TOKEN" port="$PORT" bind=0.0.0.0
}

vm_ip() {
  # Pick the first global-scope IPv4 reported for the VM. With LXD's default
  # bridge that's the lxdbr0-allocated address.
  lxc list "$1" --format json \
    | jq -r '.[0].state.network
             | to_entries[]
             | select(.key|test("^lo$")|not)
             | .value.addresses[]
             | select(.family=="inet" and .scope=="global")
             | .address' \
    | head -1
}

wait_healthz() {
  local url="$1"
  local deadline=$(( $(date +%s) + 60 ))
  until curl -fsS "$url" >/dev/null 2>&1; do
    [ "$(date +%s)" -lt "$deadline" ] \
      || die "timed out waiting for $url"
    sleep 1
  done
}

# ---- 3. Launch + install -----------------------------------------------------
ensure_vm "$NAME_A"
ensure_vm "$NAME_B"
wait_ready "$NAME_A"
wait_ready "$NAME_B"
install_snap "$NAME_A"
install_snap "$NAME_B"

IP_A="$(vm_ip "$NAME_A")"
IP_B="$(vm_ip "$NAME_B")"
[ -n "$IP_A" ] || die "could not determine IPv4 of $NAME_A"
[ -n "$IP_B" ] || die "could not determine IPv4 of $NAME_B"
log "$NAME_A IP = $IP_A"
log "$NAME_B IP = $IP_B"

# ---- 4. Configure seed (VM-A) -----------------------------------------------
log "Configuring $NAME_A as the fleet seed…"
lxc exec "$NAME_A" -- snap set fleetmind \
  fleet=true \
  advertise-url="http://$IP_A:$PORT"
# Clear any stale join-url left over from previous runs (idempotency).
lxc exec "$NAME_A" -- snap unset fleetmind join-url 2>/dev/null || true
lxc exec "$NAME_A" -- snap restart fleetmind >/dev/null
wait_healthz "http://$IP_A:$PORT/healthz"

# ---- 5. Configure joiner (VM-B) ---------------------------------------------
log "Configuring $NAME_B to join the fleet via $NAME_A…"
lxc exec "$NAME_B" -- snap set fleetmind \
  fleet=true \
  advertise-url="http://$IP_B:$PORT" \
  join-url="http://$IP_A:$PORT"
lxc exec "$NAME_B" -- snap restart fleetmind >/dev/null
wait_healthz "http://$IP_B:$PORT/healthz"

# ---- 6. Verify the mesh ------------------------------------------------------
log "Waiting for the mesh to converge to 2 members…"
deadline=$(( $(date +%s) + 60 ))
while :; do
  count="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
            "http://$IP_A:$PORT/fleet/peers" \
            | jq '.peers | length' 2>/dev/null || echo 0)"
  [ "$count" = "2" ] && break
  [ "$(date +%s)" -lt "$deadline" ] \
    || die "mesh did not reach 2 members (current: $count). Check 'lxc exec $NAME_A -- snap logs fleetmind'."
  sleep 1
done

ROSTER="$(curl -fsS -H "Authorization: Bearer $TOKEN" "http://$IP_A:$PORT/fleet/peers")"

# ---- 7. Output ---------------------------------------------------------------
echo
echo "✓ Fleet up. Members:"
echo "$ROSTER" | jq -r '.peers[] | "  - \(.node_id)  \(.advertise_url)"'
echo
echo "Seed (VM-A) bearer token:"
echo "  $TOKEN"
echo
cat <<EOF
Wire VM-A into Claude Code on this laptop:

  claude mcp add --transport http --scope project fleetmind \\
    http://$IP_A:$PORT/mcp \\
    --header "Authorization: Bearer $TOKEN"

Optional (pre-approve every fleetmind tool — they are all read-only):
  Add to .claude/settings.json:
    { "permissions": { "allow": ["mcp__fleetmind"] } }

Inside Claude Code:
  /mcp                              → should show 'fleetmind: connected'
  Ask: "list every MCP server in the fleet"
       → Claude calls list_fleet and returns both VMs.

Teardown when you're done:
  scripts/fleet-down-lxd.sh --name-a $NAME_A --name-b $NAME_B
EOF
