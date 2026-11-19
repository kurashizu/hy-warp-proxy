# 客户端懒人包(虚拟机 WARP 专用网络)

装在**宿主机**上(Linux / macOS / Windows 都行),给 QEMU 虚拟机提供一块虚拟网卡。
虚拟机的所有流量只能走 Hysteria2 隧道,从 Cloudflare WARP 出口出去;连不上或出口不是 WARP 就**直接断网**,没有第三种状态。

## 怎么用(最省事)
把整个文件夹丢给 agent,说"按 SKILL.md 部署"。agent 会自动识别系统、安装、给出 QEMU 参数并做验证。

## 手动用
1. 编辑 `settings.env`,填服务端的 `SERVER_HOST`(IP)、`AUTH_PASSWORD`、`CERT_PIN`(服务端安装完会生成 `/root/hy-client-settings.env`,可直接 `--settings-from` 导入)。
2. 安装:
   - Linux / macOS:`./install.sh`
   - Windows:`powershell -ExecutionPolicy Bypass -File .\install.ps1`
3. 安装结束会打印 QEMU 网络参数,**只**用它们给虚拟机配网(不能再加 `-nic user`、tap、第二块网卡)。每台虚拟机的 `mac=` 要不同。
4. 随时检查:`hygw -config <settings.env> -status`(退出码 0=出口是 WARP,3=闸门关闭,1=没运行)。
5. 卸载:`./uninstall.sh`(Windows:`uninstall.ps1`)。

## 它是怎么保证的
- 不用 TAP/网桥/NAT/路由:虚拟机网卡通过 QEMU 的 socket 网卡接到宿主机上的 hygw(用户态虚拟网络),宿主机上没有任何可以"回落直连"的通路。
- 虚拟机只看到私有网段 `10.77.0.0/24`,看不到宿主机的真实 IP、网卡、局域网。内网/回环/链路本地地址一律拒绝。
- hygw 只会向宿主机网络发一种包:到服务端的隧道本身,而且 `SERVER_HOST` 必须是 IP,从不使用 DNS 解析。
- 每 5 秒通过隧道向 Cloudflare 自检,必须看到 `warp=on` 才放行;检测失败立即掐断全部连接并拒绝新连接。
- hygw 崩溃或被停止,虚拟机网卡没有对端,同样断网。

## 已实测 / 未实测
实测(Linux 宿主机 + QEMU/KVM + Alpine 虚拟机,对真实服务端):
DHCP、TCP/UDP/DNS 经隧道、出口为 WARP;内网/宿主机/网关地址全部不可达;封锁服务端后虚拟机立即断网、解封后自动恢复;
服务端出口不是 WARP 时保持断网;密码错、证书指纹错保持断网;FEC 模式;`install.sh`、systemd 用户服务、卸载。
**未实测**:macOS 与 Windows 的安装脚本和运行(仅交叉编译通过)、arm64 宿主机、libvirt/UTM 的接入。

## 限制
- 虚拟机只有 IPv4 的 TCP/UDP。不转发 ICMP(ping 外网不通)。
- 代理服务端(以及到它的链路)一定看得到宿主机的真实 IP,这是任何代理都绕不开的;保证的是**虚拟机和它访问的网站**看不到。
- 不支持无法使用 QEMU socket 网卡的虚拟化软件(VirtualBox、VMware、Hyper-V、WSL2 等)。
- FEC(UDPspeeder)只有 linux-amd64、macOS、windows-amd64 的二进制。
