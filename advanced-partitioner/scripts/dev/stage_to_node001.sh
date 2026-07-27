#!/usr/bin/env bash
# Stage the Go installer toolbox to node001:/tmp/install_files.
# Use this during active development when the node is already parked in the
# sleep 10000 partscript and you want to swap in a freshly built binary
# without rebooting.
#
# Usage: scripts/stage_to_node001.sh [node]
set -euo pipefail
IFS=$'\n\t'

NODE="${1:-node001}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REMOTE_BASE="/tmp/install_files"

# Build linux/amd64 binaries first; the makefile target is idempotent.
( cd "$ROOT" && make build-linux )

# scp works controller -> node (only node -> controller is blocked).
ssh -o BatchMode=yes -o StrictHostKeyChecking=no "$NODE" \
    "mkdir -p ${REMOTE_BASE}/bin ${REMOTE_BASE}/runtime ${REMOTE_BASE}/logs"

scp -O -q \
    "${ROOT}/bin/lpart-discover-linux-amd64" \
    "${ROOT}/bin/lpart-storage-prepare-linux-amd64" \
    "${ROOT}/bin/lpart-storage-check-linux-amd64" \
    "${ROOT}/bin/lpart-osimage-install-linux-amd64" \
    "${ROOT}/bin/lpart-bootloader-finalise-linux-amd64" \
    "${NODE}:${REMOTE_BASE}/bin/"

ssh -o BatchMode=yes -o StrictHostKeyChecking=no "$NODE" "
    cd ${REMOTE_BASE}/bin && \
    for f in lpart-*-linux-amd64; do
        mv \"\$f\" \"\${f%-linux-amd64}\"
    done && chmod 0755 lpart-*
"

echo "Staged Go binaries to ${NODE}:${REMOTE_BASE}/bin/"
