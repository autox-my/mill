#!/usr/bin/env bash
# Reproducible mill LXC creation on Proxmox VE. Run on PVE as root.
set -euo pipefail

CTID=122
TEMPLATE="local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst"

pct create "$CTID" "$TEMPLATE" \
  --hostname mill --cores 1 --memory 512 --swap 256 \
  --rootfs local-lvm:8 \
  --net0 name=eth0,bridge=vmbr0,ip=192.168.0.122/24,gw=192.168.0.1,firewall=1 \
  --unprivileged 1 --features nesting=1 --onboot 1 --start 1

# TUN passthrough required for tailscaled inside unprivileged LXC.
cat >> "/etc/pve/lxc/${CTID}.conf" <<'EOF'
lxc.cgroup2.devices.allow: c 10:200 rwm
lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file
EOF

pct stop "$CTID" && pct start "$CTID"

pct exec "$CTID" -- bash -c 'apt-get update && apt-get install -y curl ca-certificates sqlite3'
pct exec "$CTID" -- bash -c 'curl -fsSL https://tailscale.com/install.sh | sh'
echo "Next: pct exec $CTID -- tailscale up --hostname=mill"
