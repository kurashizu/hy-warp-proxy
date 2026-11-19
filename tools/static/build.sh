set -ex
apk add --no-cache build-base linux-headers bison flex curl xz pkgconf libmnl-dev libmnl-static libnftnl-dev gmp-dev jansson-dev bash perl
cd /tmp
# wireguard-tools: wg
curl -fsSLO https://git.zx2c4.com/wireguard-tools/snapshot/wireguard-tools-1.0.20210914.tar.xz
tar xf wireguard-tools-*.tar.xz; cd wireguard-tools-*/src
make LDFLAGS=-static -j4 wg; strip wg; cp wg /out/wg; cp wg-quick/linux.bash /out/wg-quick; cd /tmp
# iproute2: ip, tc
curl -fsSLO https://mirrors.edge.kernel.org/pub/linux/utils/net/iproute2/iproute2-6.10.0.tar.xz
tar xf iproute2-*.tar.xz; cd iproute2-*
./configure >/dev/null 2>&1 || true
make -j4 SHARED_LIBS=n LDFLAGS=-static CCOPTS="-O2" NO_ARPD=1 >/tmp/ip.log 2>&1 || { tail -30 /tmp/ip.log; exit 1; }
strip ip/ip tc/tc; cp ip/ip /out/ip; cp tc/tc /out/tc; cd /tmp
# nftables
curl -fsSLO https://www.netfilter.org/pub/nftables/nftables-1.1.0.tar.xz
tar xf nftables-1.1.0.tar.xz; cd nftables-1.1.0
./configure --disable-shared --enable-static --without-cli --disable-python --disable-man-doc LDFLAGS=-static >/dev/null
make -j4 LDFLAGS="-static -all-static" >/tmp/nft.log 2>&1 || { tail -30 /tmp/nft.log; exit 1; }
strip src/nft; cp src/nft /out/nft
ls -l /out
