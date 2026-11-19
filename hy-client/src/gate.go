package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	log "github.com/sirupsen/logrus"
)

// gate is the single switch that decides whether the VM has any network at all.
// It is open only while a recent check, made THROUGH the tunnel, proved that the
// exit is Cloudflare WARP. Anything else (server down, auth failed, exit not WARP,
// check stale) closes it: all tracked flows are torn down and new ones are refused.
type gate struct {
	mu       sync.Mutex
	validity time.Duration
	okUntil  time.Time
	exitIP   string
	reason   string
	flows    map[io.Closer]struct{}
	changed  func(open bool)
	wasOpen  bool
}

func newGate(validity time.Duration) *gate {
	return &gate{validity: validity, flows: map[io.Closer]struct{}{}, reason: "starting"}
}

func (g *gate) open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.okUntil)
}

// track registers a live flow; returns false if the gate is closed.
func (g *gate) track(c io.Closer) (untrack func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !time.Now().Before(g.okUntil) {
		return nil, false
	}
	g.flows[c] = struct{}{}
	return func() { g.mu.Lock(); delete(g.flows, c); g.mu.Unlock() }, true
}

func (g *gate) report(ip string, err error) {
	g.mu.Lock()
	var toClose []io.Closer
	if err == nil {
		g.okUntil = time.Now().Add(g.validity)
		g.exitIP, g.reason = ip, "ok"
	} else {
		g.okUntil = time.Time{}
		g.exitIP, g.reason = "", err.Error()
		for c := range g.flows {
			toClose = append(toClose, c)
		}
		g.flows = map[io.Closer]struct{}{}
	}
	open := err == nil
	changed := open != g.wasOpen
	g.wasOpen = open
	g.mu.Unlock()

	for _, c := range toClose {
		_ = c.Close()
	}
	if changed {
		if open {
			log.Infof("GATE OPEN: exit is Cloudflare WARP (%s)", ip)
		} else {
			log.Warnf("GATE CLOSED: VM has no network (%v); cut %d flows", err, len(toClose))
		}
	}
}

func (g *gate) snapshot() (open bool, ip, reason string, flows int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.okUntil), g.exitIP, g.reason, len(g.flows)
}

// checkExit asks Cloudflare, through the tunnel, where we come out.
func checkExit(c client.Client) (string, error) {
	type res struct {
		ip  string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		raw, err := c.TCP("1.1.1.1:443")
		if err != nil {
			ch <- res{"", fmt.Errorf("tunnel: %w", err)}
			return
		}
		defer raw.Close()
		_ = raw.SetDeadline(time.Now().Add(8 * time.Second))
		tc := tls.Client(raw, &tls.Config{ServerName: "1.1.1.1", MinVersion: tls.VersionTLS12})
		if err := tc.Handshake(); err != nil {
			ch <- res{"", fmt.Errorf("tls via tunnel: %w", err)}
			return
		}
		fmt.Fprintf(tc, "GET /cdn-cgi/trace HTTP/1.1\r\nHost: 1.1.1.1\r\nUser-Agent: hygw\r\nConnection: close\r\n\r\n")
		var ip, warp string
		sc := bufio.NewScanner(tc)
		for sc.Scan() {
			l := sc.Text()
			if v, ok := strings.CutPrefix(l, "ip="); ok {
				ip = v
			}
			if v, ok := strings.CutPrefix(l, "warp="); ok {
				warp = v
			}
		}
		if warp != "on" && warp != "plus" {
			ch <- res{"", fmt.Errorf("exit is NOT WARP (warp=%q ip=%q)", warp, ip)}
			return
		}
		ch <- res{ip, nil}
	}()
	select {
	case r := <-ch:
		return r.ip, r.err
	case <-time.After(12 * time.Second):
		return "", fmt.Errorf("exit check timed out")
	}
}

func healthLoop(c client.Client, g *gate, every time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ip, err := checkExit(c)
		g.report(ip, err)
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// blocked: destinations that must never be forwarded (host/LAN/loopback/multicast...).
var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"224.0.0.0/4", "240.0.0.0/4",
	} {
		_, n, _ := net.ParseCIDR(s)
		out = append(out, n)
	}
	return out
}()

func blocked(ip net.IP) bool {
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
