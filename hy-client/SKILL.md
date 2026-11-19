---
name: hy-client
description: Deploy the hygw gateway on the user's HOST machine (Linux, macOS or Windows) so QEMU virtual machines get a virtual NIC whose only path to the internet is a Hysteria2 tunnel exiting through Cloudflare WARP. The VM is either fully on the WARP exit or fully offline; the host's real IP is never visible to it. Use when the user hands over this bundle, or asks to put VMs behind the WARP proxy / make a VM's traffic transparent through the proxy / kill-switch a VM's network.
---

# hy-client — WARP-only network for VMs (host side)

## The one invariant

> A VM attached to hygw reaches the internet **only through the Hysteria2 tunnel, and only while an
> exit check made through that tunnel proves the exit is Cloudflare WARP (`warp=on`).
> In every other state the VM has no network at all.** There is no third state.

How that holds (so you can reason about it, and so you never break it):

- hygw is a **user-space virtual network**: the VM's NIC is a QEMU socket netdev wired to hygw. There is no TAP,
  bridge, NAT or host route involved, so nothing on the host can "fall back" to the real connection.
- The VM gets a private subnet (default `10.77.0.0/24`) from hygw's own DHCP server. The host's real IP,
  interfaces, LAN and loopback do not exist from the VM's point of view; private/loopback/link-local/multicast
  destinations are refused by hygw.
- Every VM TCP/UDP flow is carried by the tunnel. The only thing hygw ever sends on the host network is the
  tunnel itself to `SERVER_HOST`. It never resolves names (`SERVER_HOST` must be an IP literal).
- A **gate** is opened only while a check *through the tunnel* to Cloudflare returns `warp=on`
  (checked every 5 s, valid 12 s). Server down, wrong password, wrong cert pin, exit not WARP, stale check
  → gate closes → all VM flows are cut and new ones refused.
- If hygw stops or crashes, the VM's NIC has no peer → no network.

## Rules for you (the agent) — do not violate

1. **Never give the VM any network device other than the hygw one.** No `-nic user`, no `-netdev user`, no `tap`,
   no bridge, no second NIC, no `-net nic` default. If the user's existing VM config has any, remove them (tell the user).
2. **Never weaken the design** to "make it work": no direct fallback, no host port-forwards into the VM that
   bypass hygw, no SOCKS/HTTP proxy settings inside the VM as a substitute, no disabling the exit check.
3. **Stop and report** if the VM frontend cannot attach a QEMU socket netdev (VirtualBox, VMware, Hyper-V, WSL2,
   Parallels). Do not improvise with NAT/bridge. Offer: use QEMU (or UTM/libvirt on top of QEMU) instead.
4. Treat `settings.env` as a secret (password, cert pin, speeder key). Don't print or paste it to the user unless asked.
5. Ask before changing the user's existing VM launch scripts/definitions; show the diff you propose.

## Procedure

### 0. Inputs
You need the proxy server's settings. They normally come from the **hy-server** skill's output file
`/root/hy-client-settings.env` on the VPS. If the user gave you a different file or values, use those.
`settings.env` in this bundle is pre-filled with the original server; replace it with the user's server.
Required keys: `SERVER_HOST` (IP address), `SERVER_PORT`, `AUTH_PASSWORD`, `CERT_PIN`, and for FEC also
`FEC_PORT`, `SPEEDER_KEY`. If the values are placeholders or you do not have them, ask the user. Do not guess.

### 1. Detect the host
```
uname -sm                          # Linux/Darwin + x86_64/arm64   (Windows: $env:PROCESSOR_ARCHITECTURE)
qemu-system-x86_64 --version       # or qemu-system-aarch64 / qemu-system-* actually used (Windows: qemu-system-x86_64.exe)
```
Bundled binaries: linux amd64/arm64, macOS amd64/arm64, Windows amd64/arm64. FEC (UDPspeeder) exists only for
linux-amd64, macOS (both), windows-amd64. Default is `FEC=0` (plain QUIC on `SERVER_PORT`); set `FEC=1` only if the
user suffers packet loss and the platform supports it (FEC can use up to ~2x bandwidth).

### 2. Install (user-level, no admin/root needed)
Linux / macOS (if the scripts lost their executable bit in transit, run them as `bash install.sh`):
```
cd <bundle dir>
./install.sh --settings-from /path/to/hy-client-settings.env      # drop the option if settings.env is already right
```
Windows (PowerShell):
```
powershell -ExecutionPolicy Bypass -File .\install.ps1 -SettingsFrom C:\path\hy-client-settings.env
```
The installer: copies the right binary to `~/.local/share/hygw` (Windows: `%LOCALAPPDATA%\hygw`), registers a
background service (systemd user unit / launchd agent / scheduled task) that restarts on failure, then waits up to
45 s for the gate to open and prints the QEMU network options.

Exit codes: `0` ready. `3` installed and running but the exit is **not proven WARP** (VMs have no network — safe state;
read the printed `reason`). Other: a setup error with a message.
Use `--no-service` (Linux/macOS) if the user does not want a service registered.

### 3. Attach the VM
`hygw -config <settings.env> -qemu-args` prints the exact options (the installer prints them too). Pick the line that
matches the installed QEMU version. Example (QEMU ≥ 9.2):
```
-netdev stream,id=hygw0,addr.type=inet,addr.host=127.0.0.1,addr.port=7777,server=off,reconnect-ms=1000 \
-device virtio-net-pci,netdev=hygw0,mac=52:54:00:77:00:01
```
- QEMU 7.2–9.1: use `reconnect=1` instead of `reconnect-ms=1000`. QEMU < 7.2: `-netdev socket,id=hygw0,connect=127.0.0.1:7777` (no auto-reconnect:
  if hygw restarts the VM stays offline until it is restarted — still safe).
- aarch64 `virt` machines: `-device virtio-net-device` or `virtio-net-pci` both work.
- **Give every VM a unique `mac=`** (DHCP leases are keyed by MAC; duplicates collide). Use `52:54:00:77:00:01`, `:02`, ...
- libvirt: `<interface type='client'><source address='127.0.0.1' port='7777'/><model type='virtio'/><mac address='52:54:00:77:00:01'/></interface>`
  and remove every other `<interface>`.
- UTM / virt-manager / similar: remove all network devices and pass the options above as extra QEMU arguments.
- Windows QEMU: same options; `LISTEN` must stay `tcp://`.
- Linux/macOS hardening option: `LISTEN=unix:///path/hygw.sock` (file permissions protect the endpoint) with `addr.type=unix,addr.path=...`
  (QEMU ≥ 7.2). Re-run the installer after changing `settings.env`.

Start the VM. Inside a Linux guest, DHCP gives `10.77.0.x`, gateway `10.77.0.1`, DNS `1.1.1.1`.

### 4. Verify — all of these, do not skip
On the host:
```
hygw -config <settings.env> -status        # {"gate_open":true,"exit_ip":"...", ...}   exit code 0
```
Inside the VM:
```
ip -4 addr; ip route                       # only 10.77.0.x / default via 10.77.0.1; no other interface with a real address
curl -s https://www.cloudflare.com/cdn-cgi/trace      # must show warp=on ; ip= must differ from the host's own public IP
# (run the same curl on the host once: its ip= must be different from the VM's)
nc -w3 -z 10.0.0.1 80 ; nc -w3 -z 192.168.1.1 80 ; nc -w3 -z <host LAN IP> 22      # all must fail (refused/timeout)
```
Kill-switch proof (do it once, then restore):
```
Linux:   systemctl --user stop hygw     # VM: curl must now fail immediately;   systemctl --user start hygw  → VM recovers by itself
macOS:   launchctl bootout gui/$(id -u)/com.hygw.gateway   ...   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.hygw.gateway.plist
Windows: Stop-ScheduledTask hygw-gateway   (and kill hygw.exe)  ...  Start-ScheduledTask hygw-gateway
```
If any check fails, the guarantee is not established: fix it or tell the user plainly. Do not report success.

### 5. Report to the user
State: host OS/arch, where hygw is installed, the QEMU options you applied (and the files you changed), the verified exit
IP (WARP), that the kill-switch test passed, and the limitations below.

## Troubleshooting (read `reason` from `hygw -status`; log at `<install dir>/hygw.log`)

| reason | meaning / fix |
|---|---|
| `authentication error, HTTP status code: 404` | wrong `AUTH_PASSWORD` (the server answers 404 as masquerade) |
| `server certificate does not match CERT_PIN` | wrong pin, or the server was reinstalled with a new cert; take `CERT_PIN` from the server's settings file |
| `tunnel: ... timeout` / `connect error` | server unreachable: wrong `SERVER_HOST`, UDP `SERVER_PORT` (443) blocked by the VPS firewall/security group or by this network. With FEC the port is `FEC_PORT` (8443) |
| `exit is NOT WARP (warp="off" ...)` | the server is up but its WARP egress is broken or bypassed: fix on the server (hy-server skill). The VM stays offline by design |
| `SERVER_HOST must be an IP address` | put the server's IPv4/IPv6 address, not a hostname |
| installer exit `3` right after install | usually one of the above; also check the clock (QUIC/TLS) |
| VM has IP but nothing works | gate closed (see status), or the VM uses another DNS/proxy setting that does not go via the VM network |
| FEC does not start | no speederv2 binary for the platform → set `FEC=0` |

## Known limits (state them honestly)

- **The proxy server (and the network path to it) sees the host's real IP.** That is inherent to using any proxy. What is
  guaranteed is that *the VM and the sites it visits* never see it.
- hygw runs on the host and sees the VM's traffic in clear text before it is tunnelled. It is the user's own machine.
- IPv4 TCP and UDP only. No ICMP (ping to the internet fails; only the gateway answers), no IPv6 for the VM.
- The VM cannot reach the host, the host's LAN, or other private ranges (by design).
- Fingerprints inside the VM (time zone, language, hardware IDs) are outside this tool's scope.
- Windows and macOS installers were written but **not run** by the author; Linux install, service, and the full
  VM test (including fail-closed behaviour) were run for real. If a Windows/macOS step fails, report it, don't hack around the invariant.

## Uninstall
`./uninstall.sh` (Linux/macOS) or `powershell -File .\uninstall.ps1`. Attached VMs simply lose their network.

## Files
`install.sh|ps1`, `uninstall.sh|ps1`, `settings.env`, `bin/` (hygw per platform, speederv2 per platform),
`src/` (Go source of hygw; `build.sh` rebuilds all platforms with Go ≥ 1.24).
