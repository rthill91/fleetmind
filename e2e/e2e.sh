#!/bin/bash
# e2e.sh — Run FleetMind integration tests inside an ephemeral LXD system container.
#
# Usage: ./e2e/e2e.sh
# Must be run from the repository root.
set -euo pipefail

CONTAINER="fleetmind-test"
TEST_BIN="fleetmind-e2e.test"

cleanup() {
  if lxc info "${CONTAINER}" >/dev/null 2>&1; then
    echo "[e2e] Stopping container ${CONTAINER}..."
    lxc stop "${CONTAINER}" --force || true
  fi
}
trap cleanup EXIT

echo "[e2e] Building test binary on host..."
go test -c ./e2e -o "${TEST_BIN}"

echo "[e2e] Removing any existing ${CONTAINER} container..."
lxc stop "${CONTAINER}" --force 2>/dev/null || true
lxc delete "${CONTAINER}" 2>/dev/null || true

echo "[e2e] Launching ephemeral system container..."
lxc launch ubuntu:24.04 "${CONTAINER}" --ephemeral

echo "[e2e] Waiting for container to become ready..."
lxc exec "${CONTAINER}" -- bash -c \
  'timeout 60 bash -c "until systemctl is-system-running >/dev/null 2>&1; do sleep 2; done"' || {
    echo "[e2e] Warning: systemd did not reach running state; continuing anyway"
  }

echo "[e2e] Pushing test binary into container..."
lxc exec "${CONTAINER}" -- mkdir -p /root
cp "${TEST_BIN}" "${TEST_BIN}.tmp"
lxc file push "${TEST_BIN}.tmp" "${CONTAINER}/root/${TEST_BIN}"
lxc exec "${CONTAINER}" -- chmod +x "/root/${TEST_BIN}"
rm -f "${TEST_BIN}.tmp"

echo "[e2e] Running integration tests inside container..."
lxc exec "${CONTAINER}" -- "/root/${TEST_BIN}" -test.v -test.timeout=120s

echo "[e2e] All tests passed."
