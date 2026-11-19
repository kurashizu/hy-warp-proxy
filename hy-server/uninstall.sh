#!/usr/bin/env bash
# Remove the hysteria/speeder/WARP stack installed by install.sh
set -u
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
systemctl disable --now speederv2-server hysteria-server hysteria-guard wg-quick@warp 2>/dev/null
rm -rf /etc/systemd/system/{hysteria-server,speederv2-server}.service{,.d} /etc/systemd/system/hysteria-guard.service
/usr/local/sbin/hy-limit off >/dev/null 2>&1; ip link del ifb-hy 2>/dev/null
rm -f /usr/local/sbin/hy-limit /etc/systemd/system/wg-quick@.service
rm -rf /etc/hysteria /etc/wireguard/warp.conf /usr/local/bin/hysteria /usr/local/bin/speederv2 /etc/sysctl.d/99-hysteria.conf
/usr/local/lib/hysteria-stack/bin/nft delete table inet hyst_guard 2>/dev/null
rm -rf /usr/local/lib/hysteria-stack
systemctl daemon-reload
echo "removed (user 'hysteria' and packages were left in place)"
