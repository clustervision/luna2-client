#!/bin/bash
# drv-baseline.sh <N> <disks...> -- P0 baseline single-disk install
N=$1; shift
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CLUSTER=${CLUSTER:-lpx}; source "$HERE/nreg.sh"
source "$HERE/reg-orch.sh"
ev_init baseline
if install_clean $W/dl-single.json auto "$@"; then
  root=$(node 'findmnt -no SOURCE /'); nv=$(node 'efibootmgr | grep -c TrinityX')
  result PASS baseline "single-disk auto install boots ($root, nvram=$nv)"
else
  result FAIL baseline "single-disk auto install did not boot in 1200s"
  exit 1
fi
