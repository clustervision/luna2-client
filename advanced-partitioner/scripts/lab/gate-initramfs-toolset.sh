#!/bin/bash
# gate-initramfs-toolset.sh -- PRE-BOOT gate: prove the PACKED install
# initramfs actually carries the lpart toolset + storage tools, per osimage.
#
# WHY THIS EXISTS (the bug class it catches, before any node PXE-boots):
#   Module 60's osimage check proves the ROOTFS (/trinity/images/<img>/usr/bin)
#   carries the lpart binaries. That is necessary but NOT sufficient: what a
#   node runs at install time is the *packed initramfs*, and the packer can
#   silently drop the toolset:
#     - Ubuntu: luna packs with `mkinitramfs` (initramfs-tools), which IGNORES
#       the committed dracut 95luna module -> the initramfs has NONE of
#       lpart-phase / luna-installer.sh / lpart-* -> `/usr/bin/lpart-phase: No
#       such file` in the prescript. (OP#1)
#     - openSUSE: dracut runs, but the image shipped without jq / sgdisk, so
#       dracut_install silently omits them -> lpart dies at partition time.
#   This single gate would have caught BOTH before a single boot.
#
# It inspects the ACTUAL packed artifact luna will serve
# (`luna osimage show <img>` -> initrdfile, under /trinity/local/luna/files),
# lists its contents format-robustly, and asserts the contract that the
# 95luna dracut module-setup.sh promises (dracut_install list).
#
# Usage (run on the TrinityX controller):
#   gate-initramfs-toolset.sh [--images "compute-image ubuntu-image ..."]
#                             [--json OUT.json] [--strict-efi] [--quiet]
#   # default images: $IMAGES, else every osimage `luna osimage list` reports.
#
# Exit: 0 = every image's initramfs carries the REQUIRED set.
#       1 = at least one REQUIRED tool MISSING (the real pre-boot failure).
#       2 = could not READ an initramfs (missing decompressor/lister) -- an
#           operator problem, NOT a pass and NOT the same as a content FAIL.
#
# Non-destructive; reads packed artifacts only. No LLM, no network.

set -u
LUNA_FILES_DIR="${LUNA_FILES_DIR:-/trinity/local/luna/files}"

# ---- the CONTRACT (mirror of 95luna/module-setup.sh dracut_install) ---------
# REQUIRED: absence means the packed initramfs cannot run the lpart install.
REQ_LPART="lpart-phase lpart-node-installer lpart-storage-prepare lpart-storage-check lpart-osimage-install lpart-bootloader-finalise"
# The shared installer body is always required. The per-framework ENTRYPOINT is
# checked separately (either/or): dracut ships luna2-start.sh, initramfs-tools
# (ubuntu) ships scripts/local-premount/luna-start -- requiring the dracut name
# would false-FAIL a correct ubuntu image.
REQ_INSTALLER="luna-installer.sh"
REQ_STORAGE="sgdisk mdadm pvcreate vgcreate lvcreate mkfs.xfs mkfs.ext4 mkfs.vfat parted partprobe wipefs blkid jq lsblk dmsetup"
# RECOMMENDED: not promised by every distro module, but wanted (UEFI etc).
# efibootmgr is NOT in the redhat dracut_install list -> a FINDING, not a FAIL,
# unless --strict-efi is given (UEFI-only labs may want it hard).
REC_TOOLS="efibootmgr mkfs.fat lpart-emit lpart-tui luna-parse-cmdline.sh"

IMAGES_ARG=""; JSON_OUT=""; STRICT_EFI=0; QUIET=0
while [ $# -gt 0 ]; do case "$1" in
    --images)     IMAGES_ARG="${2:?}"; shift 2 ;;
    --json)       JSON_OUT="${2:?}"; shift 2 ;;
    --strict-efi) STRICT_EFI=1; shift ;;
    --quiet)      QUIET=1; shift ;;
    -h|--help)    sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
esac; done

say()  { [ "$QUIET" = 1 ] || printf '%s\n' "$*"; }
ok()   { [ "$QUIET" = 1 ] || printf '  \033[32mOK\033[0m    %s\n' "$*"; }
miss() {                    printf '  \033[31mMISS\033[0m  %s\n' "$*" >&2; }
warn() { [ "$QUIET" = 1 ] || printf '  \033[33mWARN\033[0m  %s\n' "$*"; }

# ---- format-robust initramfs lister ----------------------------------------
# Emits one bare relative path per line. Tries dracut lsinitrd (handles bare
# cpio + gzip/xz/zstd/lz4 + early-cpio concatenation when the decompressors are
# present), then initramfs-tools lsinitramfs, then a magic-detect manual path.
# A listing with < 20 entries is treated as "unreadable" (a decompressor that
# half-worked), so we never turn a read failure into a false MISS.
_ird_norm() { sed 's/ -> .*//' | awk 'NF{print $NF}' | sed 's#^/*##'; }
_ird_manual() {
    local f="$1" magic dec
    magic=$(od -An -tx1 -N6 "$f" 2>/dev/null | tr -d ' \n')
    case "$magic" in
        1f8b*)        dec="gzip -dc" ;;
        fd377a585a00) dec="xz -dc" ;;
        28b52ffd*)    dec="zstd -dc" ;;
        04224d18*)    dec="lz4 -dc" ;;
        425a68*)      dec="bzip2 -dc" ;;
        303730373031) dec="cat" ;;                 # "070701" ascii cpio
        5d000*)       dec="xz --format=lzma -dc" ;;
        *)            return 1 ;;
    esac
    $dec < "$f" 2>/dev/null | cpio -t 2>/dev/null | sed 's#^/*##'
}
# _ird_python: robust concatenated-cpio reader. An initramfs is a SEQUENCE of
# cpio archives (ubuntu: an uncompressed early microcode cpio THEN a compressed
# main cpio). lsinitrd on el9 lists only the FIRST (early) segment for the ubuntu
# layout (~20 entries) and would false-FAIL a perfectly good ubuntu image -- the
# exact opposite of this gate's job. This walks every segment: parses newc cpio
# in place, and decompresses the compressed main (gzip/xz/bz2 builtin;
# zstd/lz4 via the CLI ensured by installer module 00). Emits bare paths.
_ird_python() {
    command -v python3 >/dev/null 2>&1 || return 1
    python3 - "$1" <<'PY'
import sys,gzip,bz2,lzma,zlib,subprocess
d=open(sys.argv[1],'rb').read(); n=len(d)
def dec(seg):
    if seg[:2]==b'\x1f\x8b':
        try: return gzip.decompress(seg)
        except Exception:
            try: return zlib.decompressobj(16+zlib.MAX_WBITS).decompress(seg)
            except Exception: return None
    if seg[:6]==b'\xfd7zXZ\x00':
        try: return lzma.decompress(seg)
        except Exception: return None
    if seg[:3]==b'BZh':
        try: return bz2.decompress(seg)
        except Exception: return None
    if seg[:4]==b'\x28\xb5\x2f\xfd':
        try: return subprocess.run(['zstd','-dc'],input=seg,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL).stdout or None
        except Exception: return None
    if seg[:4]==b'\x04\x22\x4d\x18':
        try: return subprocess.run(['lz4','-dc'],input=seg,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL).stdout or None
        except Exception: return None
    return None
def parse(data,off):
    names=[]; m=len(data)
    while off+110<=m and data[off:off+6]==b'070701':
        try: f=[int(data[off+6+i*8:off+6+i*8+8],16) for i in range(13)]
        except ValueError: break
        namesize=f[11]; filesize=f[6]; ns=off+110
        nm=data[ns:ns+namesize-1].decode('utf-8','replace')
        pos=ns+namesize; pos=(pos+3)&~3; pos+=filesize; pos=(pos+3)&~3
        off=pos
        if nm=='TRAILER!!!': break
        names.append(nm)
    return names,off
names=[]; off=0
while off<n:
    if d[off:off+6]==b'070701':
        nm,off=parse(d,off); names+=nm
        while off<n and d[off]==0: off+=1
    else:
        raw=dec(d[off:])
        if not raw: break
        nm,_=parse(raw,0); names+=nm
        break
for x in names: print(x.lstrip('/'))
PY
}
_ird_list() {   # $1 = initrd path; prints normalized paths or returns 1
    local f="$1" out n
    out=$(_ird_python "$f" 2>/dev/null)
    n=$(printf '%s\n' "$out" | grep -c . 2>/dev/null || echo 0)
    [ "${n:-0}" -ge 20 ] && { printf '%s\n' "$out"; return 0; }
    if command -v lsinitrd >/dev/null 2>&1; then
        out=$(lsinitrd "$f" 2>/dev/null | _ird_norm)
        n=$(printf '%s\n' "$out" | grep -c . 2>/dev/null || echo 0)
        [ "${n:-0}" -ge 20 ] && { printf '%s\n' "$out"; return 0; }
    fi
    if command -v lsinitramfs >/dev/null 2>&1; then
        out=$(lsinitramfs "$f" 2>/dev/null | sed 's#^/*##')
        n=$(printf '%s\n' "$out" | grep -c . 2>/dev/null || echo 0)
        [ "${n:-0}" -ge 20 ] && { printf '%s\n' "$out"; return 0; }
    fi
    out=$(_ird_manual "$f")
    n=$(printf '%s\n' "$out" | grep -c . 2>/dev/null || echo 0)
    [ "${n:-0}" -ge 20 ] && { printf '%s\n' "$out"; return 0; }
    return 1
}
# present <name> <listfile>: basename match anywhere, allowing an NN- hook
# prefix (dracut installs hooks as e.g. 99-luna2-start.sh) and dot-in-name.
present() {
    local name="$1" lst="$2" esc
    esc=$(printf '%s' "$name" | sed 's/[.[\*^$]/\\&/g')
    grep -qE "(^|/)([0-9]+-)?${esc}\$" "$lst"
}

# ---- resolve target images --------------------------------------------------
images="$IMAGES_ARG"
[ -n "$images" ] || images="${IMAGES:-}"
if [ -z "$images" ]; then
    # parse the `luna osimage list` table: data rows are `| <#> | <name> | ...`;
    # take the name column ($3) only on rows whose $2 is a bare integer -- avoids
    # grabbing the "Osimage"/"images" header/border words.
    images=$(luna osimage list 2>/dev/null | awk -F'|' '$2 ~ /^ *[0-9]+ *$/ {gsub(/^[ \t]+|[ \t*]+$/,"",$3); print $3}' | sort -u | tr '\n' ' ')
fi
[ -n "${images// }" ] || { echo "no osimages to check (pass --images or set IMAGES)" >&2; exit 2; }

say "== initramfs-toolset gate =="
say "   files dir: $LUNA_FILES_DIR"

overall=0; readerr=0; first_json=1
[ -n "$JSON_OUT" ] && printf '{"images":[' > "$JSON_OUT"

for img in $images; do
    show=$(luna osimage show "$img" 2>/dev/null)
    [ -n "$show" ] || { miss "$img: no such osimage (luna osimage show failed)"; overall=1; continue; }
    ird=$(printf '%s\n' "$show" | awk -F'|' '/initrdfile/{gsub(/^[ \t]+|[ \t]+$/,"",$3);print $3}')
    dist=$(printf '%s\n' "$show" | awk -F'|' '/distribution/{gsub(/^[ \t]+|[ \t]+$/,"",$3);print $3}')
    say ""
    say "-- $img (distribution=${dist:-?}) --"
    [ -n "$ird" ] || { miss "$img: no initrdfile in osimage record (never packed?)"; overall=1; continue; }
    f="$LUNA_FILES_DIR/$ird"
    [ -f "$f" ] || { miss "$img: packed initramfs absent: $f"; overall=1; continue; }

    lst=$(mktemp)
    if ! _ird_list "$f" > "$lst"; then
        miss "$img: could NOT read initramfs $f -- install a decompressor (dnf install -y zstd lz4 xz cpio) or lsinitrd/lsinitramfs"
        readerr=1; rm -f "$lst"; continue
    fi
    entries=$(grep -c . "$lst")
    say "   initrd: $ird  ($entries entries)"

    img_missing=""; img_recmiss=""
    for t in $REQ_LPART $REQ_INSTALLER $REQ_STORAGE; do
        if present "$t" "$lst"; then ok "$t"; else miss "$t  <-- REQUIRED, absent from packed initramfs"; img_missing="$img_missing $t"; fi
    done
    # entrypoint: dracut luna2-start.sh OR initramfs-tools luna-start (either).
    if present "luna2-start.sh" "$lst"; then ok "luna2-start.sh (dracut entrypoint)"
    elif present "luna-start" "$lst"; then ok "luna-start (initramfs-tools entrypoint)"
    else miss "installer entrypoint (luna2-start.sh | luna-start)  <-- REQUIRED, absent"; img_missing="$img_missing entrypoint"; fi
    for t in $REC_TOOLS; do
        if present "$t" "$lst"; then :; else
            if [ "$t" = efibootmgr ] && [ "$STRICT_EFI" = 1 ]; then
                miss "efibootmgr  <-- REQUIRED (--strict-efi), absent"; img_missing="$img_missing efibootmgr"
            else
                warn "$t (recommended) not in initramfs"; img_recmiss="$img_recmiss $t"
            fi
        fi
    done

    if [ -n "$img_missing" ]; then
        printf '  \033[31mFAIL\033[0m  %s: packed initramfs is MISSING required tools:%s\n' "$img" "$img_missing" >&2
        overall=1
    else
        ok "$img: packed initramfs carries the full required lpart toolset"
    fi

    if [ -n "$JSON_OUT" ]; then
        [ "$first_json" = 1 ] || printf ',' >> "$JSON_OUT"; first_json=0
        printf '{"image":"%s","distribution":"%s","initrd":"%s","entries":%s,"missing_required":"%s","missing_recommended":"%s","pass":%s}' \
            "$img" "${dist:-}" "$ird" "${entries:-0}" "${img_missing# }" "${img_recmiss# }" \
            "$([ -z "$img_missing" ] && echo true || echo false)" >> "$JSON_OUT"
    fi
    rm -f "$lst"
done

if [ -n "$JSON_OUT" ]; then
    printf '],"reader_error":%s,"pass":%s}\n' \
        "$([ "$readerr" = 1 ] && echo true || echo false)" \
        "$([ "$overall" = 0 ] && [ "$readerr" = 0 ] && echo true || echo false)" >> "$JSON_OUT"
fi

say ""
if [ "$readerr" = 1 ]; then
    echo "GATE ERROR: at least one initramfs was unreadable (see MISS above) -- fix the controller toolchain, then re-run." >&2
    exit 2
fi
if [ "$overall" = 0 ]; then
    say "GATE PASS: every checked initramfs carries the required lpart toolset."
    exit 0
fi
echo "GATE FAIL: a packed initramfs is missing the lpart toolset -- nodes booting it will NOT install (see MISS/FAIL above)." >&2
exit 1
