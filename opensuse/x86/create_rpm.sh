#!/bin/bash
# Build the luna2-client RPM (opensuse / SLE, x86_64).
#
# Before packaging, cross-compile the advanced-partitioner localboot
# installer (the lpart-* Go binaries) and stage them into the buildroot at
# src/usr/bin/. They are listed in luna2-client.spec %files and pulled
# into the install initramfs by the 95luna dracut module (module-setup.sh).
# The binaries are build artefacts -- never committed (see repo .gitignore).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
ADVPART="$REPO_ROOT/advanced-partitioner"
TX_ARCH="amd64"
LPART_BINS="lpart-storage-prepare lpart-storage-check lpart-osimage-install lpart-bootloader-finalise lpart-node-installer lpart-emit lpart-tui"

echo "Building advanced-partitioner lpart-* binaries (${TX_ARCH})..."
make -C "$ADVPART" build-linux
install -d -m0755 "$HERE/src/usr/bin"
for c in $LPART_BINS; do
    install -m0755 "$ADVPART/bin/${c}-linux-${TX_ARCH}" "$HERE/src/usr/bin/${c}"
done

# The buildroot is the committed src/ tree (reused across builds), so clean
# rpmbuild's generated .build-id links and disable their creation to avoid
# stale "installed but unpackaged" debuginfo links failing check-files.
rm -rf "$HERE/src/usr/lib/.build-id"
# The version comes from VERSION at the repository root, as in the pipeline.
VERSION=$(tr -d '[:space:]' < "$REPO_ROOT/VERSION")
rpmbuild --define "_build_id_links none" \
    --define "luna_version ${VERSION%-*}" --define "luna_release ${VERSION#*-}" \
    --buildroot "$HERE/src" -ba luna2-client.spec
