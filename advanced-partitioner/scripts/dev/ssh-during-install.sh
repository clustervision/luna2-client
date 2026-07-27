#!/bin/bash
# ssh-during-install.sh — regression test: SSH into the *installer* must work.
#
# The install initramfs (redhat: dropbear; ubuntu: sshd) is supposed to run an
# SSH daemon so an operator can log in to a node WHILE it installs and debug a
# stuck install. This silently broke for ubuntu (modern OpenSSH refuses to start
# without /run/sshd, and the error was swallowed) — port 22 was closed for the
# whole install. This test catches that: it polls the node during an install and
# asserts SSH (port 22 + a real ssh login) is reachable at a non-final install
# stage, NOT only after the node has booted its OS.
#
# Usage:
#   ssh-during-install.sh <node>     # run it, then trigger/await an install
#
# It watches <node>'s Luna status; while the status is an install stage
# (prescript/partscript/postscript/install.*), it probes port 22 and a
# host-key-agnostic ssh. PASS on the first successful login during install;
# FAIL if the install reaches "success" without SSH ever coming up.
set -u

node="${1:?usage: ssh-during-install.sh <node>}"
LOG=/var/log/cluster-messages
deadline=$(( $(date +%s) + 600 ))   # 10 min cap

say() { echo "[$(date +%T)] $*"; }

ssh_probe() {  # port-open AND a real login (no known_hosts, no strict)
    timeout 4 bash -c "echo > /dev/tcp/$node/22" 2>/dev/null || return 1
    timeout 6 ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o ConnectTimeout=4 -o BatchMode=yes "$node" 'echo LOGIN_OK $(uname -r)' 2>/dev/null
}

say "watching $node — trigger (or continue) an install now; probing SSH during it"
saw_install=0
while [ "$(date +%s)" -lt "$deadline" ]; do
    status=$(luna node show "$node" 2>/dev/null | grep -i 'status' | grep -oiE 'install\.[a-z]+|prescript|partscript|postscript|success' | head -1)
    booted=""
    # is the node currently in the installer (not its booted OS)?
    # the install posts install.* / *script statuses; "success" + a BOOT_IMAGE/root=luna means it booted
    case "$status" in
        ""|success)
            # could be pre-install idle or post-install booted — only a problem if booted
            ;;
        *)
            saw_install=1
            say "install stage: $status — probing SSH..."
            if out=$(ssh_probe); then
                say "PASS: SSH reachable DURING install (stage=$status): $out"
                exit 0
            fi
            say "  ...port22/ssh not up yet at stage=$status"
            ;;
    esac
    # if it finished installing and booted, and we never got SSH during install -> fail
    if [ "$status" = "success" ] && [ "$saw_install" = 1 ]; then
        if ssh_probe >/dev/null; then
            say "FAIL: SSH only came up AFTER install (node booted) — not reachable during install"
            exit 1
        fi
    fi
    sleep 8
done
say "TIMEOUT: never observed an install on $node (trigger one and re-run), or SSH never reachable"
exit 2
