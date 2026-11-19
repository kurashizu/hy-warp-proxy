#!/usr/bin/env bash
# Hysteria2 + UDPspeeder(FEC) + Cloudflare WARP egress  --  one-shot server installer
# Usage: sudo ./install.sh [--regen-warp] [--new-password]
#   --regen-warp    register a NEW Cloudflare WARP device via wgcf (recommended on a new machine;
#                   do not run the bundled WARP key on two machines at once)
#   --new-password  generate a fresh hysteria password instead of the bundled one
#   --limit-up RATE / --limit-down RATE   aggregate speed limit for ALL clients together (e.g. 50mbit, 1gbit; default: unlimited).
#                   up = client->internet, down = internet->client. Change later with: sudo hy-limit set --up ... --down ... | off
#   --regen-cert    generate a fresh self-signed TLS certificate (new pin) instead of the bundled one
#   (recommended for a new VPS: --regen-warp --new-password --regen-cert, so no secret is shared with other deployments)
# On success it writes /root/hy-client-settings.env, ready to be fed to the client bundle.
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }
say() { echo -e "\n\033[1;32m==>\033[0m $*"; }

[ "$(id -u)" = 0 ] || die "run as root (sudo ./install.sh)"
cd "$(dirname "$0")"
F=$PWD/files

REGEN_WARP=0; NEW_PASS=0; REGEN_CERT=0; LIMIT_UP=; LIMIT_DOWN=
while [ $# -gt 0 ]; do
  a=$1; shift
  case $a in
    --limit-up)   LIMIT_UP=${1:-}; [ -n "$LIMIT_UP" ] || die "--limit-up needs a rate"; shift ;;
    --limit-down) LIMIT_DOWN=${1:-}; [ -n "$LIMIT_DOWN" ] || die "--limit-down needs a rate"; shift ;;
    --regen-warp) REGEN_WARP=1 ;;
    --new-password) NEW_PASS=1 ;;
    --regen-cert) REGEN_CERT=1 ;;
    -h|--help) sed -n '2,6p' "$0"; exit 0 ;;
    *) die "unknown option: $a" ;;
  esac
done

# public edition: no secrets are shipped, so every secret is always freshly generated
REGEN_WARP=1; NEW_PASS=1; REGEN_CERT=1
[ "$(uname -m)" = x86_64 ] || die "bundled binaries are x86_64 only (got $(uname -m))"
command -v systemctl >/dev/null || die "systemd required"

say "Installing bundled tools (no package manager, nothing is downloaded)"
HY=/usr/local/lib/hysteria-stack
install -d -m 755 "$HY/bin"
for t in wg wg-quick ip tc nft hytool wgcf; do install -m 755 "$F/bin/$t" "$HY/bin/$t"; done
install -m 644 "$F/etc/ca-bundle.crt" "$HY/ca-bundle.crt"
export PATH="$HY/bin:$PATH" SSL_CERT_FILE="$HY/ca-bundle.crt"
for t in bash useradd awk sed grep install mktemp; do command -v $t >/dev/null || die "required system tool missing: $t"; done
for t in wg ip tc nft hytool; do "$HY/bin/$t" $( [ $t = wg ] && echo --version || { [ $t = hytool ] && echo rand || { [ $t = nft ] && echo -v || echo -V; }; } ) >/dev/null 2>&1 || die "bundled $t does not run on this system"; done

say "Checking kernel WireGuard support"
if ip link add wgchk0 type wireguard 2>/dev/null; then ip link del wgchk0; else modprobe wireguard 2>/dev/null || true
  ip link add wgchk0 type wireguard 2>/dev/null && ip link del wgchk0 || die "kernel has no WireGuard support (need >= 5.6)"; fi

say "Creating user + installing files"
id hysteria >/dev/null 2>&1 || useradd -r -M -s "$(command -v nologin || echo /bin/false)" hysteria
install -m 755 "$F/bin/hysteria"  /usr/local/bin/hysteria
install -m 755 "$F/bin/speederv2" /usr/local/bin/speederv2
install -d -m 755 /usr/local/sbin
install -m 755 "$F/bin/hy-limit" /usr/local/sbin/hy-limit
install -d -m 755 /etc/hysteria
install -m 644 "$F/etc/guard.nft" /etc/hysteria/guard.nft
if [ "$REGEN_CERT" = 1 ]; then
  hytool cert home-n95 /etc/hysteria/server.key /etc/hysteria/server.crt
  chown hysteria:hysteria /etc/hysteria/server.key /etc/hysteria/server.crt
  chmod 600 /etc/hysteria/server.key; chmod 644 /etc/hysteria/server.crt
fi

if [ "$NEW_PASS" = 1 ]; then PASS=$(hytool rand 16); else die "no bundled password in the public edition"; fi

# ACL: never let a client reach this host's own networks / addresses
ACL=$(mktemp)
for c in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 ::1/128 fc00::/7 fe80::/10; do
  echo "    - reject($c)" >> "$ACL"; done
for a in $(ip -o addr show scope global | awk '$2!="warp"{print $4}' | cut -d/ -f1 | sort -u); do
  echo "    - reject($a)" >> "$ACL"; done
awk -v f="$ACL" '/^@ACL@$/{while((getline l<f)>0)print l;next}{print}' "$F/etc/config.yaml.tpl" \
  | sed "s/@PASSWORD@/$PASS/" > /etc/hysteria/config.yaml
rm -f "$ACL"
chown hysteria:hysteria /etc/hysteria/config.yaml; chmod 600 /etc/hysteria/config.yaml

say "Setting up WARP (WireGuard)"
install -d -m 700 /etc/wireguard
if [ "$REGEN_WARP" = 1 ]; then
  # NOTE: not /tmp -- it is often mounted noexec on hardened hosts
  T=$(mktemp -d /root/.hy-wgcf.XXXXXX); chmod 700 "$T"
  install -m 755 "$HY/bin/wgcf" "$T/wgcf"   # bundled; only api.cloudflareclient.com must be reachable
  ( cd "$T" && ./wgcf register --accept-tos && ./wgcf generate ) || die "wgcf could not register a WARP device (see output above)"
  [ -s "$T/wgcf-profile.conf" ] || die "wgcf produced no profile"
  { echo "[Interface]"
    grep -E '^(Table|PostUp|PreDown|PostDown)' "$F/etc/warp.conf"
    sed -n '/^\[Interface\]/,$p' "$T/wgcf-profile.conf" | sed '1d' | grep -v -E '^(DNS|Table|PostUp|PreDown|PostDown)'
  } > "$T/warp.new"
  grep -q '^PrivateKey' "$T/warp.new" && grep -q '^\[Peer\]' "$T/warp.new" || die "generated warp.conf is incomplete"
  install -m 600 "$T/warp.new" /etc/wireguard/warp.conf
  mv "$T/wgcf-account.toml" /root/wgcf-account.toml 2>/dev/null || true
  rm -rf "$T"
else
  die "no bundled WARP key in the public edition"
fi
# no IPv6 on this host -> drop the v6 bits so wg-quick doesn't fail
if [ ! -e /proc/net/if_inet6 ] || [ "$(sysctl -n net.ipv6.conf.all.disable_ipv6 2>/dev/null || echo 0)" = 1 ]; then
  sed -i -E '/^(PostUp|PreDown)/ s/; *ip -6 [^;]*//g; /^Address/ s/,.*//' /etc/wireguard/warp.conf
fi
chmod 600 /etc/wireguard/warp.conf

say "Installing systemd units"
cp -a "$F"/systemd/. /etc/systemd/system/
sed -i "s/@SPEEDER_KEY@/$(hytool rand 12)/" /etc/systemd/system/speederv2-server.service
cat > /etc/sysctl.d/99-hysteria.conf <<'EOF'
net.core.rmem_max=7500000
net.core.wmem_max=7500000
EOF
sysctl --system >/dev/null 2>&1 || true
systemctl daemon-reload

if [ -n "$LIMIT_UP$LIMIT_DOWN" ]; then
  say "Configuring speed limits"
  set -- set; [ -n "$LIMIT_UP" ] && set -- "$@" --up "$LIMIT_UP"; [ -n "$LIMIT_DOWN" ] && set -- "$@" --down "$LIMIT_DOWN"
  # validate + persist only (the interface does not exist yet; PostUp applies it when warp comes up)
  HY_LIMIT_DEV=__none__ /usr/local/sbin/hy-limit "$@" >/dev/null || die "invalid speed limit (use e.g. 50mbit, 500kbit, 1gbit)"
fi

say "Starting WARP and verifying egress"
systemctl enable --now wg-quick@warp
sleep 4
TRACE=$(hytool get -t 10 -iface warp https://www.cloudflare.com/cdn-cgi/trace || true)
echo "$TRACE" | grep -q '^warp=on' || { echo "$TRACE"; die "WARP is not working (no warp=on). Try --regen-warp. Nothing else was started."; }
echo "$TRACE" | grep -E '^(ip|loc|warp)='

if [ -n "$LIMIT_UP$LIMIT_DOWN" ]; then
  say "Verifying speed limits are in force (refusing to start an unlimited server if they are not)"
  /usr/local/sbin/hy-limit apply || die "could not apply the requested speed limit; nothing else was started"
fi

say "Starting hysteria + speederv2"
systemctl enable --now hysteria-guard hysteria-server speederv2-server
sleep 3
for s in wg-quick@warp hysteria-guard hysteria-server speederv2-server; do
  systemctl is-active --quiet $s || { journalctl -u $s -n 15 --no-pager; die "$s failed to start"; }
done

say "Opening firewall ports (if a local firewall is active)"
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q active; then ufw allow 443/udp; ufw allow 8443/udp; fi
if command -v firewall-cmd >/dev/null && systemctl is-active --quiet firewalld; then
  firewall-cmd --permanent --add-port=443/udp --add-port=8443/udp && firewall-cmd --reload; fi

FP=$(hytool pin /etc/hysteria/server.crt)
KEY=$(grep -o -- '-k [^ ]*' /etc/systemd/system/speederv2-server.service | cut -d' ' -f2)
say "DONE"
PUB=$(hytool get -t 6 https://api.ipify.org 2>/dev/null || true)
[ -n "$PUB" ] || PUB=$(ip -4 route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')
( umask 077
cat > /root/hy-client-settings.env <<SETTINGS
# generated by hy-server install.sh -- merge into the client bundle (install.sh --settings-from <this file>)
SERVER_HOST=$PUB
SERVER_PORT=443
AUTH_PASSWORD=$PASS
CERT_PIN=$FP
SNI=home-n95
FEC_PORT=8443
SPEEDER_KEY=$KEY
SETTINGS
)
cat <<SUMMARY
Server addresses on this host:
$(ip -o addr show scope global | awk '$2!="warp"{print "  "$4}')
Public IPv4 seen from outside: ${PUB:-unknown}

Open UDP 443 (direct) and UDP 8443 (FEC) in your cloud security group / router port-forward.

Speed limit: $(/usr/local/sbin/hy-limit show | head -1)   (change: sudo hy-limit set --up 50mbit --down 100mbit | off)

Client settings were written to /root/hy-client-settings.env  (contains the password: treat as a secret)
  SERVER_HOST   = $PUB
  CERT_PIN      = $FP
SUMMARY
[ "$NEW_PASS" = 1 ] && echo "(password changed: update every client)"
exit 0
