---
name: hy-server
description: Deploy the Hysteria2 + UDPspeeder(FEC) + Cloudflare WARP egress proxy server onto a VPS (x86_64 Linux with systemd), fail-closed so the VPS's own IP is never used as the exit, and hand the connection settings to the hy-client bundle. Use when the user gives you this bundle together with SSH access to a VPS, or asks to set up / rebuild / uninstall the WARP proxy server.
---

# hy-server — deploy the proxy server on a VPS

## What gets installed

| Component | Role |
|---|---|
| hysteria v2.13.0 | QUIC proxy server, UDP 443, password auth, self-signed cert (clients pin its SHA-256), nginx-style 404 masquerade |
| speederv2 (20230206) | optional FEC tunnel for lossy links, UDP 8443 → 127.0.0.1:443 |
| WireGuard interface `warp` | tunnel to Cloudflare WARP; **all** egress goes out through it |
| `hy-limit` | optional aggregate speed limit (tc on the `warp` interface) |
| nft table `hyst_guard` | the `hysteria` user may only send out via `warp`/loopback (plus WARP's UDP 2408 and replies to clients) — anything else is rejected |
| systemd drop-ins | hysteria will not start without the WARP interface (fail closed) |

Result: the VPS's own public IP is never the exit. Clients' traffic exits at a Cloudflare WARP address.
The server also rejects client attempts to reach its own addresses and private ranges.

## Rules for you (the agent)

1. **Confirm the target first.** The installer installs packages, creates a user, sets sysctl, adds nft/systemd
   units and starts services. Make sure the user named this exact VPS and wants it modified. Do not run it on the user's
   own workstation by mistake.
2. Secrets: the bundle (`files/etc/default-password.txt`, `server.key`, `warp.conf`, speeder key inside the unit file)
   and the output file `/root/hy-client-settings.env` contain secrets. Don't paste them into chat unless the user
   asks; keep files mode 600; delete the uploaded bundle from the VPS after a successful install.
3. Don't open ports in the cloud provider's console/API on your own. Tell the user which ports to open
   (UDP 443, UDP 8443) unless they explicitly ask you to do it.
4. Don't weaken the fail-closed design (no direct outbound, no removing the guard) to "make it work".

## Procedure

### 0. Inputs
SSH access to the VPS: `user@host` or an ssh alias; root, or a user with passwordless `sudo`. Ask if missing.

### 1. Preflight (read-only)
```
ssh <vps> 'uname -m; . /etc/os-release; echo $PRETTY_NAME; uname -r; systemctl --version | head -1; nproc; free -m | sed -n 2p; sudo -n true && echo sudo-ok'
ssh <vps> 'ss -lun | grep -E ":(443|8443)\b" || echo ports-free'
```
Requirements: `x86_64`; systemd; kernel ≥ 5.6 (built-in WireGuard); root/sudo; Arch, Debian/Ubuntu, or Fedora-like
any distro (the installer uses **no package manager** and downloads nothing from distro repos: `wg`, `wg-quick`, `ip`, `tc`, `nft`, `wgcf`, `hytool` are bundled static binaries in `files/bin`, installed to `/usr/local/lib/hysteria-stack`; only bash, useradd, awk, sed, grep and coreutils must exist on the VPS); UDP 443/8443 not in use; the VPS can send UDP to
Cloudflare (2408). Not supported: ARM VPS, OpenVZ/LXC without WireGuard support, IPv6-only VPS.
Sizing guide (estimates, not load-tested): 1 vCPU / 512 MB is enough for 1–5 users; 2 vCPU / 1 GB for 100–300 Mbps.
FEC multiplies traffic up to ~2x and speederv2 is single-threaded, so leave bandwidth headroom.

### 2. Upload
```
scp -r <bundle dir>/hy-server <vps>:~/        # or: tar czf - hy-server | ssh <vps> 'tar xzf -'
```

### 3. Install
For a **new** VPS use all three regen flags so no secret is shared with other deployments of this bundle:
```
ssh -t <vps> 'cd ~/hy-server && sudo bash install.sh --regen-warp --new-password --regen-cert'
```
- `--regen-warp` registers a fresh Cloudflare WARP device with `wgcf` (never run the same WARP key on two machines at once).
- `--new-password` fresh hysteria password. `--regen-cert` fresh self-signed cert (new pin).
Optional **speed limit** for all clients together: add `--limit-up 50mbit --limit-down 100mbit` (up = client→internet,
down = internet→client; units `kbit|mbit|gbit`, default unlimited). Ask the user whether they want a cap and what values.
If a requested limit cannot be applied the installer aborts before starting hysteria (it will not expose an unlimited server).
Without flags the bundled secrets are reused (only OK when replacing the original machine, which must then stay off).
The script verifies WARP **before** starting hysteria; if `warp=on` cannot be shown it stops and nothing is started.
Success ends with `==> DONE` and writes `/root/hy-client-settings.env`.

### 4. Collect the client settings
```
ssh <vps> 'sudo cat /root/hy-client-settings.env' > hy-client-settings.env && chmod 600 hy-client-settings.env
```
Keys: `SERVER_HOST` (auto-detected public IPv4 — check it is right), `SERVER_PORT`, `AUTH_PASSWORD`, `CERT_PIN`, `SNI`,
`FEC_PORT`, `SPEEDER_KEY`. Hand this file to the **hy-client** skill (`install.sh --settings-from hy-client-settings.env`).

### 5. Ports
Tell the user to allow inbound **UDP 443** (and **UDP 8443** if they will use FEC) in the provider's security group /
firewall. The installer already opens them in a *local* ufw/firewalld if one is active.

### 6. Verify on the server
```
ssh <vps> 'systemctl is-active wg-quick@warp hysteria-guard hysteria-server speederv2-server'   # four lines "active"
ssh <vps> 'SSL_CERT_FILE=/usr/local/lib/hysteria-stack/ca-bundle.crt /usr/local/lib/hysteria-stack/bin/hytool get -iface warp https://www.cloudflare.com/cdn-cgi/trace | grep -E "^(ip|warp)="'   # warp=on, ip is NOT the VPS IP
ssh <vps> 'sudo nft list table inet hyst_guard'                                                  # the reject counter should stay ~0
```
The real end-to-end test is the client bundle: its `hygw -status` must show `gate_open:true` with a WARP `exit_ip`.

### 7. Speed limits (change any time)
```
ssh <vps> 'sudo hy-limit show'                                  # config + tc counters
ssh <vps> 'sudo hy-limit set --up 50mbit --down 100mbit'        # persists in /etc/hysteria/limit.env and applies now
ssh <vps> 'sudo hy-limit set --down off'                        # remove one direction;  `hy-limit off` removes both
```
Mechanism: kernel traffic control on the `warp` interface, which every client byte passes through — upload = HTB+fq_codel
on its egress, download = ingress redirected to an `ifb-hy` device and shaped there. It is re-applied automatically
whenever `wg-quick@warp` restarts (PostUp hook). The limit is **aggregate** (all clients share it), measured on payload.
With FEC the wire traffic on the public NIC is larger than the payload (up to ~1.6x at high rates, more at very low rates).
Do not use hysteria's own `bandwidth:` option for this: the hygw client uses BBR, for which the server ignores it.
Needs kernel modules `sch_htb`, `sch_fq_codel`, `cls_u32`, `act_mirred`, `ifb` (standard on mainstream distro kernels; some minimal VPS kernels lack `ifb`).

### 8. Clean up
`ssh <vps> 'rm -rf ~/hy-server'` (it holds the bundled secrets). Keep `/root/hy-client-settings.env` only as long as the user needs it.

## Troubleshooting

| Symptom | Fix |
|---|---|
| installer stops: `WARP is not working` | re-run with `--regen-warp`; if it still fails the VPS blocks UDP 2408 → try another WARP endpoint port (500, 1701, 4500) in `/etc/wireguard/warp.conf` **and** change `udp dport 2408` in `/etc/hysteria/guard.nft`, then `systemctl restart wg-quick@warp hysteria-guard hysteria-server` |
| installer aborts: `could not apply the requested speed limit` | the kernel lacks `ifb`/`sch_htb`/`cls_u32`/`act_mirred`; use a different kernel/VPS, or install without `--limit-down` and accept no download cap |
| `hysteria-server` not starting | `journalctl -u hysteria-server -n 30`; it requires `wg-quick@warp` (by design) |
| client says `exit is NOT WARP` | WARP on the server is broken/bypassed: check the `warp` interface (`wg show warp` handshake) and `nft list table inet hyst_guard` |
| client timeouts | provider firewall: UDP 443/8443 not open; or server not listening: `ss -lun` |
| `required system tool missing` / `bundled X does not run` | the VPS lacks bash/useradd/awk/sed/grep, or is not x86_64 Linux (e.g. musl-only is fine, ARM is not) |
| need to change the password later | edit `auth.password` in `/etc/hysteria/config.yaml`, restart `hysteria-server`, update clients' `AUTH_PASSWORD` |
| upgrade hysteria | replace `/usr/local/bin/hysteria` (same flags/config work), `systemctl restart hysteria-server` |

## Uninstall
`sudo ./uninstall.sh` (inside the bundle on the VPS; removes services, config, binaries, the guard table; keeps the `hysteria` user and packages).

## Notes
- The installer is idempotent; re-running re-applies config. Config lives in `/etc/hysteria/`, WARP in `/etc/wireguard/warp.conf`.
- Hysteria's ACL on the server rejects the VPS's own addresses and all private/loopback/link-local ranges, so clients cannot
  reach the VPS's internal network or services.
- The self-signed cert uses CN `home-n95` (the client's `SNI` default); it is authenticated by the pin, not by a CA.
