package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	log "github.com/sirupsen/logrus"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const gatewayMAC = "5a:94:ef:e4:0c:dd"

// Network is the virtual L2/L3 network the VM is plugged into.
// Its ONLY way out is the hysteria client (and only while the gate is open).
type Network struct {
	cfg    *Config
	stack  *stack.Stack
	sw     *tap.Switch
	hy     client.Client
	g      *gate
	pool   *tap.IPPool
	gwAddr net.IP
}

func newNetwork(cfg *Config, hy client.Client, g *gate) (*Network, error) {
	pool, err := tap.NewIPPool(cfg.Subnet)
	if err != nil {
		return nil, fmt.Errorf("subnet: %w", err)
	}
	if err := pool.Reserve(cfg.Gateway, gatewayMAC); err != nil {
		return nil, fmt.Errorf("gateway ip: %w", err)
	}
	ep, err := tap.NewLinkEndpoint(false, uint32(cfg.MTU), gatewayMAC, cfg.Gateway, nil)
	if err != nil {
		return nil, err
	}
	sw := tap.NewSwitch(false)
	ep.Connect(sw)
	sw.Connect(ep)

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4},
	})
	if e := s.CreateNIC(1, ep); e != nil {
		return nil, errors.New(e.String())
	}
	gw := net.ParseIP(cfg.Gateway).To4()
	if e := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4Slice(gw).WithPrefix(),
	}, stack.AddressProperties{}); e != nil {
		return nil, errors.New(e.String())
	}
	s.SetSpoofing(1, true)
	s.SetPromiscuousMode(1, true)
	_, ipn, err := net.ParseCIDR(cfg.Subnet)
	if err != nil {
		return nil, err
	}
	subnet, err := tcpip.NewSubnet(tcpip.AddrFromSlice(ipn.IP), tcpip.MaskFromBytes(ipn.Mask))
	if err != nil {
		return nil, err
	}
	s.SetRouteTable([]tcpip.Route{{Destination: subnet, NIC: 1}})

	n := &Network{cfg: cfg, stack: s, sw: sw, hy: hy, g: g, pool: pool, gwAddr: gw}

	tcpFwd := tcp.NewForwarder(s, 0, 256, n.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, n.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	// no ICMP forwarder on purpose: nothing but the gateway ever answers a ping.

	if err := n.startDHCP(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *Network) Accept(ctx context.Context, c net.Conn) error {
	return n.sw.Accept(ctx, c, types.QemuProtocol)
}

func (n *Network) refuse(dst tcpip.Address) bool {
	ip := net.IP(dst.AsSlice())
	if blocked(ip) || ip.Equal(n.gwAddr) {
		return true
	}
	return !n.g.open()
}

func (n *Network) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	if n.refuse(id.LocalAddress) {
		r.Complete(true) // RST: the VM sees "connection refused"
		return
	}
	target := net.JoinHostPort(net.IP(id.LocalAddress.AsSlice()).String(), strconv.Itoa(int(id.LocalPort)))
	go func() {
		type dres struct {
			c   net.Conn
			err error
		}
		ch := make(chan dres, 1)
		go func() {
			c, err := n.hy.TCP(target)
			ch <- dres{c, err}
		}()
		var out net.Conn
		select {
		case d := <-ch:
			if d.err != nil {
				log.Debugf("tcp %s: %v", target, d.err)
				r.Complete(true)
				return
			}
			out = d.c
		case <-time.After(20 * time.Second):
			go func() { // late completion: drop it
				if d := <-ch; d.c != nil {
					d.c.Close()
				}
			}()
			r.Complete(true)
			return
		}
		untrackOut, ok := n.g.track(out)
		if !ok {
			out.Close()
			r.Complete(true)
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		r.Complete(false)
		if terr != nil {
			untrackOut()
			out.Close()
			return
		}
		in := gonet.NewTCPConn(&wq, ep)
		untrackIn, ok := n.g.track(in)
		if !ok {
			untrackOut()
			out.Close()
			in.Close()
			return
		}
		var wg sync.WaitGroup
		wg.Add(2)
		pipe := func(dst, src net.Conn) {
			defer wg.Done()
			_, _ = io.Copy(dst, src)
			if cw, ok := dst.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			} else {
				_ = dst.Close()
			}
		}
		go pipe(out, in)
		go pipe(in, out)
		wg.Wait()
		untrackIn()
		untrackOut()
		in.Close()
		out.Close()
	}()
}

func (n *Network) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	ip := net.IP(id.LocalAddress.AsSlice())
	if id.LocalAddress == tcpip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return true // DHCP/broadcast handled elsewhere
	}
	if n.refuse(id.LocalAddress) {
		return true // silently dropped
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		return false
	}
	guest := gonet.NewUDPConn(&wq, ep)
	dst := net.JoinHostPort(ip.String(), strconv.Itoa(int(id.LocalPort)))
	idle := 60 * time.Second
	if id.LocalPort == 53 {
		idle = 8 * time.Second
	}
	go n.relayUDP(guest, ep, dst, idle)
	return true
}

func (n *Network) relayUDP(guest *gonet.UDPConn, ep tcpip.Endpoint, dst string, idle time.Duration) {
	defer ep.Close()
	defer guest.Close()
	if !n.g.open() {
		return
	}
	hu, err := n.hy.UDP()
	if err != nil {
		log.Debugf("udp %s: %v", dst, err)
		return
	}
	defer hu.Close()
	untrack, ok := n.g.track(&multiCloser{guest, hu})
	if !ok {
		return
	}
	defer untrack()

	var peer atomic.Value // net.Addr of the guest socket
	done := make(chan struct{})
	go func() { // tunnel -> guest
		defer close(done)
		for {
			data, _, err := hu.Receive()
			if err != nil {
				return
			}
			_ = guest.SetReadDeadline(time.Now().Add(idle))
			pa, _ := peer.Load().(net.Addr)
			if pa == nil {
				continue
			}
			if _, err := guest.WriteTo(data, pa); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 65535)
	for {
		_ = guest.SetReadDeadline(time.Now().Add(idle))
		nr, from, err := guest.ReadFrom(buf)
		if err != nil {
			return
		}
		peer.Store(from)
		if !n.g.open() {
			return
		}
		if err := hu.Send(buf[:nr], dst); err != nil {
			return
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

type multiCloser [2]io.Closer

func (m *multiCloser) Close() error {
	for _, c := range m {
		_ = c.Close()
	}
	return nil
}

// ---- DHCP (own copy: hands out public DNS, the gateway itself serves nothing else) ----

func (n *Network) startDHCP() error {
	var wq waiter.Queue
	ep, e := n.stack.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	if e != nil {
		return errors.New(e.String())
	}
	ep.SocketOptions().SetBroadcast(true)
	if e := ep.Bind(tcpip.FullAddress{NIC: 1, Port: 67}); e != nil {
		ep.Close()
		return errors.New(e.String())
	}
	ln := gonet.NewUDPConn(&wq, ep)

	_, ipn, _ := net.ParseCIDR(n.cfg.Subnet)
	h := func(conn net.PacketConn, peer net.Addr, m *dhcpv4.DHCPv4) {
		reply, err := dhcpv4.NewReplyFromRequest(m)
		if err != nil {
			return
		}
		switch m.MessageType() {
		case dhcpv4.MessageTypeDiscover:
			reply.UpdateOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeOffer))
		case dhcpv4.MessageTypeRequest:
			reply.UpdateOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeAck))
		default:
			return
		}
		ip, err := n.pool.GetOrAssign(m.ClientHWAddr.String())
		if err != nil {
			log.Errorf("dhcp: %v", err)
			return
		}
		reply.YourIPAddr = net.IP(ip.AsSlice())
		reply.UpdateOption(dhcpv4.OptServerIdentifier(n.gwAddr))
		reply.UpdateOption(dhcpv4.OptIPAddressLeaseTime(time.Hour))
		reply.UpdateOption(dhcpv4.OptSubnetMask(ipn.Mask))
		reply.UpdateOption(dhcpv4.OptRouter(n.gwAddr))
		reply.UpdateOption(dhcpv4.OptDNS(n.cfg.DNS...))
		reply.UpdateOption(dhcpv4.Option{Code: dhcpv4.OptionInterfaceMTU, Value: dhcpv4.Uint16(uint16(n.cfg.MTU))})
		if _, err := conn.WriteTo(reply.ToBytes(), peer); err != nil {
			log.Debugf("dhcp reply: %v", err)
		}
	}
	srv, err := server4.NewServer("", nil, h, server4.WithConn(ln))
	if err != nil {
		return err
	}
	go func() { log.Debugf("dhcp server stopped: %v", srv.Serve()) }()
	return nil
}
