#!/bin/bash
# fleet-down-lxd.sh — Tear down the VMs created by fleet-up-lxd.sh.
#
# Usage: scripts/fleet-down-lxd.sh [flags]
#   --name-a NAME   seed VM name        (default: fleetmind-a)
#   --name-b NAME   joiner VM name      (default: fleetmind-b)
#   -h, --help      show help
set -euo pipefail

NAME_A="fleetmind-a"
NAME_B="fleetmind-b"

usage() { sed -n '2,7p' "$0"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --name-a) NAME_A="$2"; shift 2 ;;
    --name-b) NAME_B="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

log() { printf '[fleet-down] %s\n' "$*" >&2; }

teardown() {
  local name="$1"
  if lxc info "$name" >/dev/null 2>&1; then
    log "Stopping $name…"
    lxc stop "$name" --force >/dev/null 2>&1 || true
    log "Deleting $name…"
    lxc delete "$name" >/dev/null
  else
    log "$name not present — skipping."
  fi
}

teardown "$NAME_A"
teardown "$NAME_B"
log "Done."
