#!/usr/bin/env python3
"""Generate installer-flow-redhat.svg — the Chapter 6 install-flow figure.

A single, color-blocked vertical figure that follows a RHEL node from power-on
to first boot. Hand-tuned SVG is error-prone, so we lay it out programmatically:
one row per install.sh step, color-coded by *kind*, with the three Luna scripts
(prescript / partscript / postscript) expanded into their lpart worker sub-steps
on the right. Reproducible: edit here, re-run, rebuild the PDF.

Colour legend (kept in sync with the doc's CSS theme):
  framework  grey   – install.sh plumbing
  script     amber  – the node/group prescript/partscript/postscript (an lpart-phase shim)
  lpart      green  – an lpart-* worker doing real install work
  ramdisk    blue   – luna2-start.sh, outside install.sh
  controller slate  – the Luna daemon
  no-op      faint  – legacy step neutralised under lpart
"""
import pathlib

OUT = pathlib.Path(__file__).resolve().parent / "installer-flow-redhat.svg"

# ---- palette -------------------------------------------------------------
INK = "#0f172a"
C = {
    "fw":   ("#f1f5f9", "#94a3b8", "#334155"),   # fill, stroke, text-accent
    "col":  ("#fef3c7", "#f59e0b", "#b45309"),
    "lpart":("#d1fae5", "#10b981", "#047857"),
    "ram":  ("#dbeafe", "#3b82f6", "#1d4ed8"),
    "ctl":  ("#e2e8f0", "#64748b", "#334155"),
    "noop": ("#f8fafc", "#cbd5e1", "#94a3b8"),
    "ok":   ("#dcfce7", "#16a34a", "#15803d"),
}
STATUS_FILL, STATUS_TEXT = "#ede9fe", "#6d28d9"

# ---- canvas --------------------------------------------------------------
W = 800
LX, LW = 34, 386          # left ladder x, width
RX = LX + LW + 16         # right (lpart) zone x  = 436
RW = W - RX - 18          # right zone width
RH, GAP = 38, 7
PITCH = RH + GAP

svg = []
def e(s): svg.append(s)

def box(x, y, w, h, kind, rx=7, dash=False, sw=1.4):
    f, s, _ = C[kind]
    d = ' stroke-dasharray="5 3"' if dash else ''
    e(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" '
      f'fill="{f}" stroke="{s}" stroke-width="{sw}"{d}/>')

def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

def txt(x, y, s, size=12, fill=INK, anchor="start", weight="normal", mono=False, italic=False):
    fam = ' font-family="SFMono-Regular,Consolas,monospace"' if mono else ''
    it = ' font-style="italic"' if italic else ''
    e(f'<text x="{x}" y="{y}" font-size="{size}" fill="{fill}" '
      f'text-anchor="{anchor}" font-weight="{weight}"{fam}{it}>{esc(s)}</text>')

def chip(x, y, w, h, label, sub=None):
    f, s, t = C["lpart"]
    e(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="6" fill="{f}" stroke="{s}" stroke-width="1.2"/>')
    txt(x + 9, y + (16 if sub else h/2 + 4), label, 11, t, weight="bold", mono=True)
    if sub:
        txt(x + 9, y + 30, sub, 8.8, "#475569")

# =========================================================================
# header
# =========================================================================
e(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} 1118" '
  f'font-family="Helvetica,Arial,sans-serif">')
e(f'<rect x="0" y="0" width="{W}" height="1118" fill="white"/>')
txt(LX, 26, "How a node installs: power-on → install.sh → first boot", 17, INK, weight="bold")
txt(LX, 44, "RHEL / dracut 95luna line. Each install.sh step is one block, coloured by who runs it.", 10.5, "#6b7280")

# =========================================================================
# boot chain (horizontal)
# =========================================================================
by = 60
chain = [
    ("Firmware", "DHCP / PXE"),
    ("iPXE", "chain to controller"),
    ("Controller", "discover_mac → netboot?"),
    ("kernel + initramfs", "cmdline: root=luna …"),
]
cw = (W - LX - 18 - 3 * 10) / 4
for i, (a, b) in enumerate(chain):
    x = LX + i * (cw + 10)
    kind = "ctl" if i == 2 else "fw"
    box(x, by, cw, 40, kind)
    txt(x + cw/2, by + 17, a, 11, C[kind][2], anchor="middle", weight="bold")
    txt(x + cw/2, by + 31, b, 9, "#475569", anchor="middle")
    if i < 3:
        ax = x + cw
        e(f'<line x1="{ax}" y1="{by+20}" x2="{ax+10}" y2="{by+20}" stroke="#64748b" stroke-width="1.6"/>')

# =========================================================================
# ramdisk OPEN bracket
# =========================================================================
ry = by + 56
box(LX, ry, W - LX - 18, 46, "ram", rx=9, sw=1.6)
txt(LX + 14, ry + 19, "▶  install ramdisk — luna2-start.sh  (owns console, network, SSH, the fetch/run/retry loop, the pivot)",
    11.5, C["ram"][2], weight="bold")
txt(LX + 14, ry + 35, "brings up NIC + network · starts dropbear sshd + tty2 login · fetches TPM token · curl /boot/install/<node> → install.sh",
    9.3, "#1e3a8a")

# =========================================================================
# install.sh ladder
# =========================================================================
# rows: (kind, n, title, detail, status, lpart=[(label, sub), ...])
rows = [
    ("fw",   "1",  "lunainit", "create runtime + /sysroot dirs", None, None),
    ("fw",   "2",  "dynamic_ip_check", "if DHCP, report the leased IP", None, None),
    ("fw",   "3",  "node_scripts", "stage any boot-script plugins", "install.scripts", None),
    ("col",  "4",  "prescript", "node/group prescript → lpart-phase pre", "install.prescript",
        [("prepare-runtime", "make runtime / sysroot scaffolding")]),
    ("fw",   "5",  "write_provisioning_inputs", "storage.json · osimage-filter.json · install-context.json", None, None),
    ("fw",   "6",  "bmcsetup", "configure BMC — IPMI user + network", "install.setupbmc", None),
    ("col",  "7",  "partscript", "node/group partscript → lpart-phase part (writes the disk)", "install.partscript",
        [("install-context", "resolve mode + disklayout"),
         ("storage-prepare", "discover · partition · RAID · LVM · mkfs · mount"),
         ("osimage-install", "download · extract → sysroot · verify")]),
    ("noop", "8–9", "download_image · unpack_imagefile", "no-ops under lpart (owned by osimage-install)", None, None),
    ("fw",   "10–11", "collect_mac_n_name_net · change_net", "hostname + ifcfg into the new OS", "install.setnet", None),
    ("fw",   "12", "node_secrets", "fetch node secrets into the new OS", "install.secrets", None),
    ("col",  "13", "postscript", "node/group postscript → lpart-phase post (makes it bootable)", "install.postscript",
        [("bootloader-finalise", "fstab · shim+GRUB → ESP · UEFI NVRAM · verify")]),
    ("fw",   "14", "node_roles", "install + enable role units", "install.roles", None),
    ("fw",   "15–17", "fix_capabilities · restore_selinux_context", "setcap · SELinux relabel · update_system_info (asset tag)", None, None),
    ("ok",   "18", "cleanup  +  success", "clear scratch, post final state", "install.success", None),
]

y = ry + 60
ladder_top = y - 6
for kind, n, title, detail, status, lpart in rows:
    h = RH
    # extra height for multi-substep part row handled by chips on the right
    box(LX, y, LW, h, kind)
    # number badge
    f, s, t = C[kind]
    txt(LX + 13, y + h/2 + 4, n, 11, t, anchor="middle", weight="bold")
    e(f'<line x1="{LX+26}" y1="{y+6}" x2="{LX+26}" y2="{y+h-6}" stroke="{s}" stroke-width="1"/>')
    bold = kind in ("col", "ok")
    txt(LX + 34, y + 16, title, 11.5, t if bold else INK, weight="bold" if bold else "bold", mono=(kind in ("fw","noop","ok")))
    italic = kind == "noop"
    txt(LX + 34, y + 30, detail, 8.9, "#94a3b8" if kind == "noop" else "#475569", italic=italic)
    # status tag on the far right of the ladder
    if status:
        tw = 9 + len(status) * 5.0
        tx = LX + LW - tw - 8
        e(f'<rect x="{tx}" y="{y+7}" width="{tw}" height="15" rx="7.5" fill="{STATUS_FILL}"/>')
        txt(tx + tw/2, y + 17.6, status, 8.2, STATUS_TEXT, anchor="middle", mono=True)
    # connector to next
    if kind != "ok":
        cx = LX + 13
        e(f'<line x1="{cx}" y1="{y+h}" x2="{cx}" y2="{y+h+GAP}" stroke="#cbd5e1" stroke-width="1.4"/>')

    # lpart expansion chips on the right zone, vertically centred on this row band
    if lpart:
        nsub = len(lpart)
        ch = 38 if any(sub for _, sub in lpart) else 26
        block_h = nsub * ch + (nsub - 1) * 6
        cy = y + h/2 - block_h/2
        # bracket from the amber row into the green zone
        e(f'<path d="M{LX+LW} {y+h/2} H{RX-8}" stroke="{C["col"][1]}" stroke-width="1.6" fill="none"/>')
        e(f'<circle cx="{LX+LW}" cy="{y+h/2}" r="2.4" fill="{C["col"][1]}"/>')
        for label, sub in lpart:
            chip(RX, cy, RW, ch, label, sub)
            cy += ch + 6
    y += PITCH

ladder_bot = y - GAP

# =========================================================================
# ramdisk CLOSE bracket
# =========================================================================
fy = y + 4
box(LX, fy, W - LX - 18, 44, "ram", rx=9, sw=1.6)
txt(LX + 14, fy + 18, "◀  back to luna2-start.sh  —  no reboot inside install.sh", 11.5, C["ram"][2], weight="bold")
txt(LX + 14, fy + 34, "on success: move install log → /sysroot/var/log, then  switch_root /sysroot  — the node's first boot of its installed OS",
    9.3, "#1e3a8a")

# =========================================================================
# legend
# =========================================================================
ly = fy + 60
txt(LX, ly, "Legend", 11, INK, weight="bold")
legend = [
    ("fw", "framework", "install.sh plumbing"),
    ("col", "Luna scripts", "pre / part / post"),
    ("lpart", "lpart worker", "real install work"),
    ("ram", "ramdisk", "luna2-start.sh"),
    ("noop", "no-op", "legacy step retired"),
]
lx = LX
ly2 = ly + 10
for kind, name, desc in legend:
    box(lx, ly2, 16, 16, kind, rx=4, sw=1.2)
    txt(lx + 22, ly2 + 8, name, 9.5, C[kind][2], weight="bold")
    txt(lx + 22, ly2 + 19, desc, 8, "#6b7280")
    lx += 124
# status tag legend
e(f'<rect x="{lx}" y="{ly2}" width="16" height="16" rx="8" fill="{STATUS_FILL}"/>')
txt(lx + 22, ly2 + 8, "install.<stage>", 9.5, STATUS_TEXT, weight="bold")
txt(lx + 22, ly2 + 19, "status to controller", 8, "#6b7280")

e('</svg>')
out = "\n".join(svg)
# Emit non-ASCII glyphs as XML numeric entities so the SVG is encoding-proof in
# every renderer (literal UTF-8 was double-decoded to mojibake by one path).
for ch, ent in {"→": "&#8594;", "▶": "&#9654;", "◀": "&#9664;",
                "—": "&#8212;", "·": "&#183;", "…": "&#8230;"}.items():
    out = out.replace(ch, ent)
OUT.write_text(out, encoding="utf-8")
print("wrote", OUT, len(out), "bytes; ladder", round(ladder_top), "->", round(ladder_bot))
