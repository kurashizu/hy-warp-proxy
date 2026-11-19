package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read from a KEY=VALUE file (same format as settings.env).
type Config struct {
	ServerHost string // IP literal only: the host must never resolve names
	ServerPort int    // hysteria UDP port (direct mode)
	Auth       string
	CertPin    string // sha256 of server leaf cert, hex, colons optional
	SNI        string

	FEC       bool
	FECPort   int    // server-side speederv2 port
	FECLocal  string // local speederv2 listen addr
	FECKey    string
	FECBin    string
	FECParams string

	Listen   string // qemu netdev endpoint: tcp://127.0.0.1:7777 or unix:///path
	Subnet   string
	Gateway  string
	MTU      int
	DNS      []net.IP
	Interval time.Duration // exit check interval
	Validity time.Duration // gate stays open this long after a successful check

	StatusFile string
	LogLevel   string
	LogFile    string
}

func defaults() *Config {
	return &Config{
		ServerPort: 443, SNI: "home-n95",
		FECPort: 8443, FECLocal: "127.0.0.1:4443",
		FECParams: "1:31,2:30,4:28,8:24,13:19,20:12",
		Listen:    "tcp://127.0.0.1:7777",
		Subnet:    "10.77.0.0/24", Gateway: "10.77.0.1", MTU: 1400,
		DNS:      []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("1.0.0.1")},
		Interval: 5 * time.Second, Validity: 12 * time.Second,
		LogLevel: "info",
	}
}

func loadConfig(path string) (*Config, error) {
	c := defaults()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "SERVER_HOST":
			c.ServerHost = v
		case "SERVER_PORT":
			c.ServerPort, err = strconv.Atoi(v)
		case "AUTH_PASSWORD":
			c.Auth = v
		case "CERT_PIN":
			c.CertPin = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(v, ":", ""), "-", ""))
		case "SNI":
			c.SNI = v
		case "FEC":
			c.FEC = v == "1" || strings.EqualFold(v, "yes") || strings.EqualFold(v, "true")
		case "FEC_PORT":
			c.FECPort, err = strconv.Atoi(v)
		case "FEC_LOCAL":
			c.FECLocal = v
		case "SPEEDER_KEY":
			c.FECKey = v
		case "SPEEDER_BIN":
			c.FECBin = v
		case "FEC_PARAMS":
			c.FECParams = v
		case "LISTEN":
			c.Listen = v
		case "SUBNET":
			c.Subnet = v
		case "GATEWAY_IP":
			c.Gateway = v
		case "MTU":
			c.MTU, err = strconv.Atoi(v)
		case "DNS":
			c.DNS = nil
			for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
				ip := net.ParseIP(s)
				if ip == nil || ip.To4() == nil {
					return nil, fmt.Errorf("DNS: %q is not an IPv4 address", s)
				}
				c.DNS = append(c.DNS, ip)
			}
		case "CHECK_INTERVAL":
			var s int
			s, err = strconv.Atoi(v)
			c.Interval = time.Duration(s) * time.Second
		case "STATUS_FILE":
			c.StatusFile = v
		case "LOG_LEVEL":
			c.LogLevel = v
		case "LOG_FILE":
			c.LogFile = v
		default:
			// ignore keys that belong to the launcher scripts
		}
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %s: %w", path, n, k, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return c, c.validate()
}

func (c *Config) validate() error {
	if ip := net.ParseIP(c.ServerHost); ip == nil {
		return fmt.Errorf("SERVER_HOST must be an IP address (got %q): the gateway never uses the host resolver", c.ServerHost)
	}
	if c.Auth == "" {
		return fmt.Errorf("AUTH_PASSWORD is empty")
	}
	if len(c.CertPin) != 64 {
		return fmt.Errorf("CERT_PIN must be a sha256 fingerprint (64 hex chars)")
	}
	if c.FEC && (c.FECKey == "" || c.FECBin == "") {
		return fmt.Errorf("FEC=1 needs SPEEDER_KEY and SPEEDER_BIN")
	}
	if len(c.DNS) == 0 {
		return fmt.Errorf("DNS list is empty")
	}
	if c.MTU < 576 || c.MTU > 1500 {
		return fmt.Errorf("MTU out of range")
	}
	if c.Validity < 2*c.Interval {
		c.Validity = 2*c.Interval + 2*time.Second
	}
	return nil
}

// target is what the hysteria client dials.
func (c *Config) target() *net.UDPAddr {
	if c.FEC {
		a, _ := net.ResolveUDPAddr("udp", c.FECLocal)
		return a
	}
	return &net.UDPAddr{IP: net.ParseIP(c.ServerHost), Port: c.ServerPort}
}
