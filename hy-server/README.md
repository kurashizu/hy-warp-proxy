# 服务端懒人包:Hysteria2 + UDPspeeder(FEC) + Cloudflare WARP 出口

> 给 agent 用:把整个文件夹和 VPS 的 SSH 访问方式交给 agent,说"按 SKILL.md 部署"。

## 一键安装
```bash
tar xzf hy-server.tgz && cd hy-server
sudo ./install.sh                 # 沿用包里的 WARP 配置和密码
sudo ./install.sh --regen-warp --new-password --regen-cert   # 新 VPS 推荐:全部换新,不与其他部署共用任何密钥
sudo ./install.sh --regen-warp --new-password --regen-cert --limit-up 50mbit --limit-down 100mbit   # 带限速(所有客户端合计)
sudo ./install.sh --regen-warp    # 只重新注册一个 WARP 设备(用 wgcf)
sudo ./install.sh --new-password  # 只生成新的 hysteria 密码(客户端要同步更新)
sudo ./install.sh --regen-cert    # 只生成新的自签证书(客户端 CERT_PIN 要同步更新)
```
装完会生成 `/root/hy-client-settings.env`(含服务器 IP、密码、证书指纹、speeder key),
客户端包用 `./install.sh --settings-from hy-client-settings.env` 直接导入。
卸载:`sudo ./uninstall.sh`。

## 包含什么
| 组件 | 作用 |
|---|---|
| hysteria v2.13.0 | QUIC 代理服务端,UDP 443 |
| speederv2 (20230206) | FEC 前向纠错隧道,UDP 8443 -> 127.0.0.1:443 |
| WireGuard (`warp` 网卡) | 连 Cloudflare WARP,所有出口流量走这里 |
| nft 兜底 (`hysteria-guard`) | hysteria 用户的流量只能走 warp/回环,防止直出泄露本机 IP |
| systemd 依赖 | WARP 起不来 -> hysteria 不启动(失败即关闭) |

## 要求
- x86_64 Linux + systemd,内核 >= 5.6(自带 WireGuard),需要 root
- 发行版:任意(只要 x86_64 + systemd + bash)。**安装过程不使用任何发行版包管理器**:`wg`/`wg-quick`/`ip`/`tc`/`nft`/`wgcf`/`hytool`(替代 curl+openssl)都是包内静态二进制,装到 `/usr/local/lib/hysteria-stack`;系统只需已有 bash、useradd、awk、sed、grep、coreutils。联网需求仅 `--regen-warp` 时访问 Cloudflare API(不加则完全离线)
- 放行 **UDP 443**(直连)和 **UDP 8443**(FEC);云主机要在安全组里放行,家里要在路由器做端口转发
- 能访问 Cloudflare WARP(UDP 2408 出站)

## 限速
`--limit-up`(客户端→互联网)和 `--limit-down`(互联网→客户端)是**所有客户端合计**的速度上限,单位 `kbit|mbit|gbit`,默认不限速。
随时可改:`sudo hy-limit set --up 50mbit --down 100mbit`,看状态 `sudo hy-limit show`,取消 `sudo hy-limit off`(或只取消一边:`--down off`)。
实现是内核 tc 作用在 `warp` 网卡上(上行 HTB+fq_codel,下行走 ifb 入口整形),WARP 重启会自动重新应用。
限的是负载速率;开了 FEC 后公网网卡上的实际流量会比这个更大。不用 hysteria 自带的 `bandwidth` 选项:本方案的客户端使用 BBR,服务端会忽略它。
已在隔离网络命名空间里实测:设 20/40/5 Mbit,实测 19.3/38.4/4.9 Mbit;取消后恢复约 16 Gbit,非法速率会被拒绝,ifb 设备会清理。

## 性能需求
实测数据来自原机器 home-n95(Intel N95 4 核 / 15GB,运行 16 天,期间 WARP 隧道累计下行约 51GB、上行约 8GB):
- 空闲负载 0.09;hysteria 约 30–70MB 内存,speederv2 约 25MB
- 16 天累计 CPU 时间:hysteria 约 2.2 小时,speederv2 约 2.7 小时,平均各占不到一个核的 1%

建议配置(**以下为估算,没有做过压测**):

| 场景 | CPU | 内存 | 带宽 |
|---|---|---|---|
| 个人 / 1–5 人轻度使用 | 1 核 | 512MB | 上下行各 ≥ 你想要的速度 |
| 多人 / 100–300 Mbps | 2 核 | 1GB | 同上 |
| 更高吞吐 | 4 核 | 2GB | 同上 |

注意:
- **FEC 会放大带宽**。`-f1:31,2:30,…,20:12` 是按每 10ms 窗口内的包数分档:流量很小时冗余倍数极大(单包最多 32 倍,但绝对量小),流量大时约 1.6 倍。所以走 FEC 时,链路上下行至少留出目标速度的 **2 倍**余量。直连模式(443)没有这个放大。
- speederv2 是**单线程**,FEC 流量很大时它会先成为瓶颈;hysteria 本身可以用多核。
- WARP 隧道 MTU 为 1280,会有少量开销,速度上限也取决于 Cloudflare。
- 磁盘占用 < 100MB,没有持续写盘。

## 注意
- 包里有密码、证书私钥、WireGuard 私钥和 speeder key,别外传。
- 同一个 WARP 密钥不要在两台机器同时用,会互相顶掉。新机器请用 `--regen-warp`,旧机器之后不再启动就可以沿用。
- **已在干净的 Debian 12(systemd 容器)上端到端实测**:`--regen-warp --new-password --regen-cert --limit-up 8mbit --limit-down 16mbit` 一次装完,
  新注册的 WARP 设备出口正常(`warp=on`),四个服务起来,客户端包的 `hygw` 连上后闸门打开且出口是 WARP;真实隧道里限速实测下载 15.0 / 上传 7.5 Mbit/s(设 16 / 8),
  取消限速后 225 / 46.7 Mbit/s;`uninstall.sh` 清理干净。这次测试还抓到并修复了一个 bug(`/tmp` 挂载为 noexec 时 wgcf 无法运行)。
- **没测过**:Ubuntu / Fedora / Arch 上的安装(已在不含 curl/openssl/ip/nft/wg 的干净 Debian 12 容器里跑通)、真实 VPS 内核(`ifb` 等限速模块缺失时安装器会中止并提示)、无 IPv6 主机的精简逻辑。
- 官方已有 hysteria v2.14.0,包里是 2.13.0(与旧服务端一致)。要升级就替换 `/usr/local/bin/hysteria` 后重启服务。
