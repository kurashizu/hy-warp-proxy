#!/usr/bin/env bash
# Install the hygw gateway on a Linux or macOS HOST (user-level; no root needed).
#   ./install.sh               install + start as a background service, then wait until the exit is proven WARP
#   ./install.sh --no-service  install and run in the background without registering a service
#   ./install.sh --settings-from FILE   first merge SERVER_HOST/PORT, AUTH_PASSWORD, CERT_PIN, SNI, FEC_PORT, SPEEDER_KEY
#                                       from FILE (the server's /root/hy-client-settings.env) into settings.env
# Env: HYGW_HOME=<dir> overrides the install dir (default ~/.local/share/hygw)
set -euo pipefail
cd "$(dirname "$0")"
die() { echo "ERROR: $*" >&2; exit 1; }
say() { echo -e "\n\033[1;32m==>\033[0m $*"; }

NO_SERVICE=0; FROM=
while [ $# -gt 0 ]; do
  a=$1; shift
  case $a in
    --settings-from) FROM=${1:-}; [ -n "$FROM" ] && [ -f "$FROM" ] || die "--settings-from needs an existing file"; shift ;;
    --no-service) NO_SERVICE=1 ;;
    -h|--help) sed -n 2,6p "$0"; exit 0 ;;
    *) die "unknown option: $a" ;;
  esac
done

case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) die "this script is for Linux/macOS; on Windows use install.ps1" ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die "unsupported CPU: $(uname -m)" ;; esac
[ -f "bin/hygw-$os-$arch" ] || die "no binary for $os-$arch in bin/"
[ -f settings.env ] || die "settings.env is missing"
if [ -n "$FROM" ]; then
  for k in SERVER_HOST SERVER_PORT AUTH_PASSWORD CERT_PIN SNI FEC_PORT SPEEDER_KEY; do
    v=$(grep -E "^$k=" "$FROM" | tail -1 | cut -d= -f2- || true)
    [ -n "$v" ] || continue
    if grep -q "^$k=" settings.env; then sed -i.bak "s|^$k=.*|$k=$v|" settings.env && rm -f settings.env.bak; else echo "$k=$v" >> settings.env; fi
  done
  echo "merged server settings from $FROM"
fi

DEST=${HYGW_HOME:-$HOME/.local/share/hygw}
say "Installing to $DEST ($os-$arch)"
mkdir -p "$DEST"; chmod 700 "$DEST"
if [ -f "$DEST/settings.env" ] && ! cmp -s settings.env "$DEST/settings.env"; then
  cp -p "$DEST/settings.env" "$DEST/settings.env.bak-$(date +%s)"
fi
# stop a running instance first so the binary can be replaced
if [ "$os" = linux ] && command -v systemctl >/dev/null 2>&1; then systemctl --user stop hygw.service 2>/dev/null || true; fi
if [ "$os" = darwin ]; then launchctl bootout "gui/$(id -u)/com.hygw.gateway" 2>/dev/null || true; fi
[ -f "$DEST/hygw.pid" ] && kill "$(cat "$DEST/hygw.pid")" 2>/dev/null || true

install -m 755 "bin/hygw-$os-$arch" "$DEST/hygw.new"; mv -f "$DEST/hygw.new" "$DEST/hygw"
install -m 600 settings.env "$DEST/settings.env"
[ "$os" = darwin ] && xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true

set_kv() { # set_kv KEY VALUE  (idempotent edit of $DEST/settings.env)
  local f="$DEST/settings.env"
  if grep -q "^$1=" "$f"; then sed -i.bak "s|^$1=.*|$1=$2|" "$f" && rm -f "$f.bak"; else echo "$1=$2" >> "$f"; fi
}
set_kv LOG_FILE "$DEST/hygw.log"
set_kv STATUS_FILE "$DEST/hygw.status.json"

FEC=$(grep -E '^FEC=' "$DEST/settings.env" | tail -1 | cut -d= -f2 || true)
if [ "$FEC" = 1 ]; then
  case "$os-$arch" in
    linux-amd64) sp=speederv2_amd64 ;;
    darwin-amd64) sp=speederv2_mac ;;
    darwin-arm64) sp=speederv2_mac_m1 ;;
    *) die "FEC (UDPspeeder) has no binary for $os-$arch. Set FEC=0 in settings.env and re-run." ;;
  esac
  install -m 755 "bin/$sp" "$DEST/speederv2"
  [ "$os" = darwin ] && xattr -dr com.apple.quarantine "$DEST/speederv2" 2>/dev/null || true
  set_kv SPEEDER_BIN "$DEST/speederv2"
fi

CMD=("$DEST/hygw" -config "$DEST/settings.env")
say "Validating settings"
"${CMD[@]}" -qemu-args >/dev/null || die "settings.env is invalid (see message above)"

if [ "$NO_SERVICE" = 1 ]; then
  say "Starting in the background (no service)"
  nohup setsid "${CMD[@]}" >/dev/null 2>&1 &
  echo $! > "$DEST/hygw.pid"
elif [ "$os" = linux ]; then
  say "Registering systemd user service"
  [ -n "${XDG_RUNTIME_DIR:-}" ] || { [ -d "/run/user/$(id -u)" ] && export XDG_RUNTIME_DIR="/run/user/$(id -u)"; }
  command -v systemctl >/dev/null || die "systemd not found; re-run with --no-service"
  mkdir -p "$HOME/.config/systemd/user"
  cat > "$HOME/.config/systemd/user/hygw.service" <<EOF
[Unit]
Description=hygw: WARP-only transparent gateway for VMs
After=network.target

[Service]
ExecStart=$DEST/hygw -config $DEST/settings.env
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable hygw.service >/dev/null 2>&1
  systemctl --user restart hygw.service
  # linger = keep the user service running after logout / at boot (uninstall.sh reverts this)
  loginctl enable-linger "$USER" 2>/dev/null || echo "note: could not enable linger; the gateway will only run while you are logged in"
else
  say "Registering launchd agent"
  P="$HOME/Library/LaunchAgents/com.hygw.gateway.plist"
  mkdir -p "$HOME/Library/LaunchAgents"
  cat > "$P" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.hygw.gateway</string>
  <key>ProgramArguments</key><array><string>$DEST/hygw</string><string>-config</string><string>$DEST/settings.env</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>2</integer>
</dict></plist>
EOF
  launchctl bootout "gui/$(id -u)" "$P" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$P"
fi

say "Waiting for the tunnel and the WARP exit check (up to 45s)"
rc=1
for _ in $(seq 1 23); do
  sleep 2
  if "${CMD[@]}" -status >/dev/null 2>&1; then rc=0; break; fi
done
"${CMD[@]}" -status || true
if [ $rc -ne 0 ]; then
  echo
  echo "The gateway is installed and running, but the exit is NOT proven to be WARP yet."
  echo "That is the safe state: a VM attached to it has no network. Look at the reason above and at $DEST/hygw.log"
  exit 3
fi

say "READY. Gateway is up and the exit is Cloudflare WARP."
echo "Attach VMs with these QEMU network options (and NO other network device):"
echo
"${CMD[@]}" -qemu-args
echo
echo "Check any time:  $DEST/hygw -config $DEST/settings.env -status   (exit 0 = WARP ok, 3 = gate closed, 1 = not running)"
