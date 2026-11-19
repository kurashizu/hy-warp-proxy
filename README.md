# hy-warp-proxy

Hysteria2 (+ optional UDPspeeder FEC) proxy server whose **only** exit is Cloudflare WARP, plus a cross-platform
client gateway (`hygw`) that gives QEMU VMs a virtual NIC which is either fully on the WARP exit or fully offline.

- `hy-server/` – server installer for an x86_64 Linux VPS with systemd.  Uses kernel WireGuard if present, otherwise the bundled userspace `wireguard-go`. No package manager is used: static
  `wg wg-quick ip tc nft wgcf hytool` are bundled in the release. Fresh password, cert and WARP device on every install.
  Optional aggregate speed limit (`--limit-up/--limit-down`). Has a `SKILL.md` so an agent can deploy it.
- `hy-client/` – `hygw` (Go source in `src/`), installers for Linux/macOS/Windows, and a `SKILL.md` so an agent can
  attach VMs to it.
- `tools/static/` – how the static helper binaries were built (Alpine/musl) and the `hytool` source.

**This repository holds sources and docs only.** Prebuilt binaries are in the
[Releases](../../releases): `hy-server.tgz` and `hy-client.zip` are the ready-to-use bundles. Hand a bundle to an agent
and point it at the `SKILL.md` inside.

No credentials are published here: `hy-client/settings.env` has placeholders; the server generates its own secrets.
Status: server installer tested on Debian 12 (systemd container); Windows/macOS client scripts and non-Debian server
distros are untested. See the READMEs.
