# node-parameterized helpers. Usage: [CLUSTER=lpx] N=2 source nreg.sh
# CTRL resolves from the democluster /etc/hosts entry "<cluster>.lab";
# override with CTRL=<ip> (old trx lab: CLUSTER=trx CTRL=192.168.123.217).
CLUSTER=${CLUSTER:-lpx}
N=${N:?set N}
if [ -z "${CTRL:-}" ]; then
  CTRL=$(awk -v h="${CLUSTER}.lab" '$2==h{print $1}' /etc/hosts | tail -1)
fi
: "${CTRL:?no CTRL set and no ${CLUSTER}.lab entry in /etc/hosts}"
DOM=$CLUSTER-node00$N; NODE=node00$N
NVRAM=/var/lib/libvirt/qemu/nvram/${DOM}_VARS.fd
diskfile(){ [ "$1" = vda ] && echo /var/lib/libvirt/images/clones/$DOM.qcow2 || echo /var/lib/libvirt/images/scratch/$DOM-$1.qcow2; }
node(){ timeout 45 ssh -o StrictHostKeyChecking=no root@$CTRL "ssh -o StrictHostKeyChecking=no -o ConnectTimeout=6 $NODE '$1'" 2>/dev/null; }
ctrl(){ timeout 45 ssh -o StrictHostKeyChecking=no root@$CTRL "$@"; }
blank(){ for d in "$@"; do f=$(diskfile $d); case $d in
    vda) qemu-img create -f raw "$f" 13528858624 >/dev/null 2>&1;;
    vd[b-z]) qemu-img create -f qcow2 "$f" 8G >/dev/null 2>&1;; esac
    chown qemu:qemu "$f" 2>/dev/null; done; }
reset_nvram(){ rm -f "$NVRAM"; }
setcfg(){ ctrl "luna node change $NODE $*" 2>&1 | grep -qiE 'updated|nothing is changed' || echo "setcfg WARN not confirmed: $*" >&2; }
pushdl(){ ctrl "cat > /tmp/dl-$NODE.json" < "$1"; setcfg "-qdisklayout /tmp/dl-$NODE.json"; }
shot(){ local p=$1; [ "${p#/}" = "$p" ] && p=${LAB_WORK:-$HOME/lpart-uefi-work}/$p; virsh screenshot $DOM "$p.ppm" >/dev/null 2>&1 && convert "$p.ppm" "$p.png" 2>/dev/null; }
fresh(){ virsh destroy $DOM >/dev/null 2>&1; sleep 1; reset_nvram; blank "$@"; virsh start $DOM >/dev/null 2>&1; }
wait_boot(){ local end=$((SECONDS+${1:-1000})); while [ $SECONDS -lt $end ]; do node 'findmnt -no SOURCE /' | grep -q os-root && return 0; sleep 20; done; return 1; }
