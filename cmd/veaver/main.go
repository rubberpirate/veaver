//go:build linux

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rubberpirate/veaver/internal/console"
	"github.com/rubberpirate/veaver/internal/platform"
	"github.com/rubberpirate/veaver/internal/service"
	"github.com/rubberpirate/veaver/internal/transport"
	"github.com/rubberpirate/veaver/internal/web"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "veaver:", e)
		os.Exit(1)
	}
}
func defaults() (string, string) {
	if os.Geteuid() == 0 {
		return "/etc/veaver/config.json", "/var/lib/veaver"
	}
	dir, e := os.UserConfigDir()
	if e != nil {
		dir = "."
	}
	state, e := os.UserCacheDir()
	if e != nil {
		state = "."
	}
	return filepath.Join(dir, "veaver", "config.json"), filepath.Join(state, "veaver")
}
func run(args []string) error {
	if len(args) == 0 {
		return tui(nil)
	}
	switch args[0] {
	case "tui":
		return tui(args[1:])
	case "serve":
		return serve(args[1:])
	case "doctor":
		return doctor()
	case "cleanup":
		return cleanup(args[1:])
	case "relay":
		return relay(args[1:])
	case "cert":
		return certificate(args[1:])
	case "version":
		fmt.Println("Veaver 0.2.0")
		return nil
	case "status", "dashboard", "plan", "apply", "restore", "refresh":
		return client(args[0], args[1:])
	case "help", "--help", "-h":
		usage()
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func cleanup(args []string) error {
	_, stateDefault := defaults()
	f := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	state := f.String("state-dir", stateDefault, "Recovery directory")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := os.MkdirAll(*state, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(*state, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("daemon is still running; use restore instead")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	r := platform.NewRouting(filepath.Join(*state, "routing.json"), platform.ExecRunner{})
	if e = r.Load(); e != nil {
		return e
	}
	return r.Cleanup(context.Background())
}
func usage() {
	fmt.Print(`Veaver — a NetworkManager extension and local network console

  veaver [tui]       Open the live terminal dashboard
  veaver serve       Start daemon (observe by default)
  veaver dashboard   Print your authenticated dashboard URL
  veaver status      Read daemon statistics as JSON
  veaver doctor      Inspect NetworkManager and kernel capabilities
  veaver plan       Review config without changing networking
  veaver apply      Apply a reviewed JSON config through the daemon
  veaver restore    Restore Veaver-owned routing to stock networking
  veaver refresh    Refresh NetworkManager state (dispatcher integration)
  veaver cert       Generate relay TLS credentials and a token
  veaver relay      Run an authenticated MPTCP TCP relay

Use 'veaver <command> -h' for flags. Direct balancing is per connection;
single-stream TCP bonding needs a relay. IPv6 and UDP relay bonding are excluded.
`)
}
func serve(args []string) error {
	configDefault, stateDefault := defaults()
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	config := f.String("config", configDefault, "Configuration JSON")
	state := f.String("state-dir", stateDefault, "Private state and recovery directory")
	listen := f.String("listen", "127.0.0.1:8787", "Loopback dashboard address")
	if e := f.Parse(args); e != nil {
		return e
	}
	host, _, e := net.SplitHostPort(*listen)
	if e != nil || host != "127.0.0.1" {
		return errors.New("dashboard must bind to 127.0.0.1:port")
	}
	if e = os.MkdirAll(*state, 0700); e != nil {
		return e
	}
	token, e := web.Token(filepath.Join(*state, "dashboard-token"))
	if e != nil {
		return e
	}
	nm, e := platform.ConnectNM()
	if e != nil {
		return e
	}
	s, e := service.New(*config, *state, nm, platform.ExecRunner{})
	if e != nil {
		nm.Close()
		return e
	}
	defer func() {
		if e := s.Close(); e != nil {
			fmt.Fprintln(os.Stderr, "routing cleanup:", e)
		}
	}()
	l, e := net.Listen("tcp4", *listen)
	if e != nil {
		return e
	}
	defer l.Close()
	actual := l.Addr().String()
	fmt.Printf("Daemon: http://%s\nAccess token file: %s\nUse 'veaver tui --state-dir %s --url http://%s' to connect.\n", actual, filepath.Join(*state, "dashboard-token"), *state, actual)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return web.New(s, token, actual).Serve(ctx, l)
}
func doctor() error {
	n, e := platform.ConnectNM()
	var network platform.Network
	detail := ""
	if e == nil {
		defer n.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		network, e = n.Snapshot(ctx)
	}
	if e != nil {
		detail = e.Error()
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"network": network, "checks": platform.Checks(network, detail)})
}
func client(command string, args []string) error {
	_, stateDefault := defaults()
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	state := f.String("state-dir", stateDefault, "Daemon state directory")
	url := f.String("url", "http://127.0.0.1:8787", "Local daemon URL")
	file := f.String("file", "", "Config JSON to plan/apply")
	if e := f.Parse(args); e != nil {
		return e
	}
	c, e := console.NewClient(*url, *state)
	if e != nil {
		return e
	}
	if command == "dashboard" {
		fmt.Println(c.DashboardURL())
		return nil
	}
	path, method, body := "status", "GET", ""
	switch command {
	case "plan", "apply":
		if *file == "" {
			return errors.New("--file CONFIG.json is required")
		}
		b, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		body = string(b)
		method = "POST"
		path = "plan"
		if command == "apply" {
			path = "config"
		}
	case "restore":
		method = "POST"
		path = "config"
		body = platform.JSON(service.DefaultConfig())
	case "refresh":
		path = "refresh"
		method = "POST"
		body = "{}"
	}
	b, e := c.Request(context.Background(), method, path, []byte(body))
	if e != nil {
		return e
	}
	_, e = os.Stdout.Write(b)
	return e
}

func tui(args []string) error {
	_, stateDefault := defaults()
	f := flag.NewFlagSet("tui", flag.ContinueOnError)
	state := f.String("state-dir", stateDefault, "Daemon state directory")
	address := f.String("url", "http://127.0.0.1:8787", "Local daemon URL")
	once := f.Bool("once", false, "Print a plain snapshot and exit")
	file := f.String("file", "", "Load a draft configuration to review in the TUI")
	if e := f.Parse(args); e != nil {
		return e
	}
	c, e := console.NewClient(*address, *state)
	if e != nil {
		return e
	}
	var draft *service.Config
	if *file != "" {
		if _, e = os.Stat(*file); e != nil {
			return e
		}
		cfg, e := service.LoadConfig(*file)
		if e != nil {
			return e
		}
		draft = &cfg
	}
	return console.Run(context.Background(), c, *once, draft)
}
func relay(args []string) error {
	f := flag.NewFlagSet("relay", flag.ContinueOnError)
	listen := f.String("listen", ":7443", "Relay listen address")
	cert := f.String("cert", "", "TLS certificate PEM")
	key := f.String("key", "", "TLS private key PEM")
	tokenFile := f.String("token-file", "", "Relay token file")
	max := f.Int("max-connections", 1024, "Maximum simultaneous relayed connections")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *max < 1 || *max > 10000 {
		return errors.New("max-connections must be 1–10000")
	}
	pair, e := tls.LoadX509KeyPair(*cert, *key)
	if e != nil {
		return e
	}
	token, e := transport.ReadToken(*tokenFile)
	if e != nil {
		return e
	}
	lc := net.ListenConfig{}
	lc.SetMultipathTCP(true)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	l, e := lc.Listen(ctx, "tcp", *listen)
	if e != nil {
		return e
	}
	defer l.Close()
	fmt.Println("Authenticated TLS/MPTCP relay listening on", l.Addr())
	s := &transport.RelayServer{TLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, Token: token, MaxConnections: *max}
	return s.Serve(ctx, l)
}
func certificate(args []string) error {
	f := flag.NewFlagSet("cert", flag.ContinueOnError)
	hostname := f.String("hostname", "", "Relay DNS name or IP")
	out := f.String("out", "./relay-credentials", "New credential directory")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *hostname == "" || strings.ContainsAny(*hostname, "\x00\r\n/") {
		return errors.New("a valid --hostname is required")
	}
	if _, e := os.Stat(*out); !os.IsNotExist(e) {
		return errors.New("credential output directory already exists; refusing to overwrite")
	}
	if e := os.MkdirAll(*out, 0700); e != nil {
		return e
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return e
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: *hostname}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	if ip := net.ParseIP(*hostname); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{*hostname}
	}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if e != nil {
		return e
	}
	pk, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return e
	}
	for name, data := range map[string][]byte{"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "key.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})} {
		if e = os.WriteFile(filepath.Join(*out, name), data, 0600); e != nil {
			return e
		}
	}
	b := make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*out, "token"), []byte(hex.EncodeToString(b)+"\n"), 0600); e != nil {
		return e
	}
	fmt.Println("Created", *out, "— copy cert.pem and token to clients; keep key.pem on the relay")
	return nil
}
