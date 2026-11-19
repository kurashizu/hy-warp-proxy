#!/usr/bin/env bash
# Remove the hygw gateway (Linux/macOS). VMs attached to it simply lose their network.
set -u
DEST=${HYGW_HOME:-$HOME/.local/share/hygw}
if [ "$(uname -s)" = Linux ]; then
  [ -n "${XDG_RUNTIME_DIR:-}" ] || { [ -d "/run/user/$(id -u)" ] && export XDG_RUNTIME_DIR="/run/user/$(id -u)"; }
  systemctl --user disable --now hygw.service 2>/dev/null
  rm -f "$HOME/.config/systemd/user/hygw.service"
  systemctl --user daemon-reload 2>/dev/null
  loginctl disable-linger "$USER" 2>/dev/null   # undo what install.sh enabled
else
  P="$HOME/Library/LaunchAgents/com.hygw.gateway.plist"
  launchctl bootout "gui/$(id -u)" "$P" 2>/dev/null
  rm -f "$P"
fi
[ -f "$DEST/hygw.pid" ] && kill "$(cat "$DEST/hygw.pid")" 2>/dev/null
pkill -f "$DEST/speederv2" 2>/dev/null
rm -rf "$DEST"
echo "removed $DEST and the service"
