// hygw: a user-space virtual network gateway for VMs.
//
// The VM plugs into it through QEMU's socket netdev (-netdev stream / socket).
// Every TCP/UDP flow the VM makes is carried through a Hysteria2 tunnel to the server,
// which exits through Cloudflare WARP. There is no other path: this program never
// dials the host network except for the tunnel to the server itself, never resolves names,
// and closes a "gate" (tearing down all flows) whenever it cannot prove, through the
// tunnel, that the exit is WARP. The VM therefore either goes out via WARP, or is offline.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/containers/gvisor-tap-vsock/pkg/transport"
	log "github.com/sirupsen/logrus"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", "settings.env", "settings file (KEY=VALUE)")
	status := flag.Bool("status", false, "print the status of a running gateway and exit")
	ver := flag.Bool("version", false, "print version")
	qargs := flag.Bool("qemu-args", false, "print the QEMU network arguments for the configured endpoint and exit")
	flag.Parse()
	if *ver {
		fmt.Println("hygw", version)
		return
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}
	if cfg.StatusFile == "" {
		cfg.StatusFile = filepath.Join(filepath.Dir(*cfgPath), "hygw.status.json")
	}
	if *status {
		printStatus(cfg.StatusFile)
		return
	}
	if *qargs {
		printQemuArgs(cfg)
		return
	}
	lvl, _ := log.ParseLevel(cfg.LogLevel)
	log.SetLevel(lvl)
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true, DisableColors: true})
	if cfg.LogFile != "" {
		if st, err := os.Stat(cfg.LogFile); err == nil && st.Size() > 5<<20 {
			_ = os.Rename(cfg.LogFile, cfg.LogFile+".old")
		}
		if f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			log.SetOutput(f)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var speeder *exec.Cmd
	if cfg.FEC {
		go superviseSpeeder(ctx, cfg)
	}

	pin, _ := hex.DecodeString(cfg.CertPin)
	hy, err := client.NewReconnectableClient(func() (*client.Config, error) {
		return &client.Config{
			ServerAddr: cfg.target(),
			Auth:       cfg.Auth,
			TLSConfig: client.TLSConfig{
				ServerName:         cfg.SNI,
				InsecureSkipVerify: true, // authenticity comes from the pin below
				VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
					if len(raw) == 0 {
						return errors.New("no server certificate")
					}
					sum := sha256.Sum256(raw[0])
					if hex.EncodeToString(sum[:]) != hex.EncodeToString(pin) {
						return errors.New("server certificate does not match CERT_PIN")
					}
					return nil
				},
			},
			QUICConfig: client.QUICConfig{DisablePathMTUDiscovery: cfg.FEC},
		}, nil
	}, func(_ client.Client, info *client.HandshakeInfo, count int) {
		log.Infof("tunnel connected (udp=%v, attempt %d)", info.UDPEnabled, count)
	}, true)
	if err != nil {
		log.Fatalf("hysteria client: %v", err)
	}
	defer hy.Close()

	g := newGate(cfg.Validity)
	stop := make(chan struct{})
	go healthLoop(hy, g, cfg.Interval, stop)
	go statusLoop(cfg.StatusFile, g, stop)

	nw, err := newNetwork(cfg, hy, g)
	if err != nil {
		log.Fatalf("network: %v", err)
	}
	ln, err := transport.Listen(cfg.Listen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Listen, err)
	}
	log.Infof("hygw %s ready: VM network %s via %s, QEMU endpoint %s, FEC=%v. VM is OFFLINE until the exit check proves WARP.",
		version, cfg.Subnet, cfg.target(), cfg.Listen, cfg.FEC)
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Errorf("accept: %v", err)
			time.Sleep(time.Second)
			continue
		}
		log.Infof("VM attached from %s", c.RemoteAddr())
		go func() {
			if err := nw.Accept(ctx, c); err != nil {
				log.Infof("VM detached: %v", err)
			}
		}()
	}
	close(stop)
	g.report("", errors.New("shutting down"))
	_ = speeder
}

func superviseSpeeder(ctx context.Context, cfg *Config) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, cfg.FECBin, "-c", "-l"+cfg.FECLocal,
			"-r"+net.JoinHostPort(cfg.ServerHost, fmt.Sprint(cfg.FECPort)),
			"-k", cfg.FECKey, "-f"+cfg.FECParams, "--mode", "0", "--mtu", "1350",
			"--timeout", "8", "-i", "10", "--disable-color")
		log.Infof("starting speederv2 (FEC) -> %s:%d", cfg.ServerHost, cfg.FECPort)
		out, _ := os.OpenFile(filepath.Join(filepath.Dir(cfg.StatusFile), "speeder.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		if out != nil {
			out.Close()
		}
		if ctx.Err() != nil {
			return
		}
		log.Warnf("speederv2 exited (%v); restarting in 2s (VM stays offline meanwhile)", err)
		time.Sleep(2 * time.Second)
	}
}

type statusDoc struct {
	Time    string `json:"time"`
	Open    bool   `json:"gate_open"`
	ExitIP  string `json:"exit_ip"`
	Reason  string `json:"reason"`
	Flows   int    `json:"flows"`
	Version string `json:"version"`
}

func statusLoop(path string, g *gate, stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		open, ip, reason, flows := g.snapshot()
		b, _ := json.Marshal(statusDoc{time.Now().Format(time.RFC3339), open, ip, reason, flows, version})
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, path)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func printStatus(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Println(`{"running":false}`)
		os.Exit(1)
	}
	var d statusDoc
	if json.Unmarshal(b, &d) != nil {
		fmt.Println(string(b))
		os.Exit(1)
	}
	t, _ := time.Parse(time.RFC3339, d.Time)
	if time.Since(t) > 10*time.Second { // stale file == gateway not running == VM has no network
		fmt.Println(`{"running":false,"note":"status file is stale"}`)
		os.Exit(1)
	}
	fmt.Println(strings.TrimSpace(string(b)))
	if !d.Open {
		os.Exit(3)
	}
}

func printQemuArgs(cfg *Config) {
	u, err := url.Parse(cfg.Listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad LISTEN:", err)
		os.Exit(2)
	}
	dev := "-device virtio-net-pci,netdev=hygw0,mac=52:54:00:77:00:01"
	fmt.Println("# Use ONLY these network options for the VM (no -nic/-net user, no tap, no second NIC).")
	fmt.Println("# Give every VM its own mac= (52:54:00:77:00:XX).")
	switch u.Scheme {
	case "tcp":
		h, p, _ := net.SplitHostPort(u.Host)
		fmt.Printf("# QEMU >= 9.2:\n-netdev stream,id=hygw0,addr.type=inet,addr.host=%s,addr.port=%s,server=off,reconnect-ms=1000 %s\n", h, p, dev)
		fmt.Printf("# QEMU 7.2 - 9.1:\n-netdev stream,id=hygw0,addr.type=inet,addr.host=%s,addr.port=%s,server=off,reconnect=1 %s\n", h, p, dev)
		fmt.Printf("# QEMU < 7.2 (no auto-reconnect):\n-netdev socket,id=hygw0,connect=%s:%s %s\n", h, p, dev)
	case "unix":
		fmt.Printf("# QEMU >= 9.2:\n-netdev stream,id=hygw0,addr.type=unix,addr.path=%s,server=off,reconnect-ms=1000 %s\n", u.Path, dev)
		fmt.Printf("# QEMU 7.2 - 9.1:\n-netdev stream,id=hygw0,addr.type=unix,addr.path=%s,server=off,reconnect=1 %s\n", u.Path, dev)
	default:
		fmt.Println("unsupported LISTEN scheme:", u.Scheme)
	}
}
