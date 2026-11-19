// hytool: tiny static helper so the server installer needs no curl/openssl.
//   hytool rand [bytes]                      random hex
//   hytool cert CN KEYFILE CRTFILE           self-signed P-256 cert, 10 years
//   hytool pin CRTFILE                       SHA-256 fingerprint, openssl style (AA:BB:..)
//   hytool get [-iface DEV] [-t SEC] URL     HTTPS/HTTP GET to stdout (nonzero exit on failure); SSL_CERT_FILE honoured
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, "hytool: "+f+"\n", a...); os.Exit(1) }

func main() {
	if len(os.Args) < 2 {
		die("usage: rand|cert|pin|get")
	}
	switch os.Args[1] {
	case "rand":
		n := 16
		if len(os.Args) > 2 {
			n, _ = strconv.Atoi(os.Args[2])
		}
		b := make([]byte, n)
		rand.Read(b)
		fmt.Println(hex.EncodeToString(b))
	case "cert":
		if len(os.Args) != 5 {
			die("cert CN KEYFILE CRTFILE")
		}
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		sn, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
		t := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: os.Args[2]},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, t, t, &k.PublicKey, k)
		if err != nil {
			die("%v", err)
		}
		kb, _ := x509.MarshalPKCS8PrivateKey(k)
		os.WriteFile(os.Args[3], pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600)
		os.WriteFile(os.Args[4], pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	case "pin":
		if len(os.Args) != 3 {
			die("pin CRTFILE")
		}
		d, err := os.ReadFile(os.Args[2])
		if err != nil {
			die("%v", err)
		}
		b, _ := pem.Decode(d)
		if b == nil {
			die("no PEM block")
		}
		h := sha256.Sum256(b.Bytes)
		var p []string
		for _, x := range h {
			p = append(p, fmt.Sprintf("%02X", x))
		}
		fmt.Println(strings.Join(p, ":"))
	case "get":
		iface, to, url := "", 10, ""
		a := os.Args[2:]
		for i := 0; i < len(a); i++ {
			switch a[i] {
			case "-iface":
				i++
				iface = a[i]
			case "-t":
				i++
				to, _ = strconv.Atoi(a[i])
			default:
				url = a[i]
			}
		}
		if url == "" {
			die("get needs a URL")
		}
		d := &net.Dialer{Timeout: time.Duration(to) * time.Second}
		if iface != "" {
			d.Control = func(_, _ string, c syscall.RawConn) error {
				var e error
				c.Control(func(fd uintptr) { e = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface) })
				return e
			}
		}
		cl := &http.Client{Timeout: time.Duration(to) * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, n, ad string) (net.Conn, error) {
				if strings.HasPrefix(n, "tcp") {
					n = "tcp4"
				}
				return d.DialContext(ctx, n, ad)
			}}}
		r, err := cl.Get(url)
		if err != nil {
			die("%v", err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			die("HTTP %d", r.StatusCode)
		}
		io.Copy(os.Stdout, r.Body)
	default:
		die("unknown command")
	}
}
