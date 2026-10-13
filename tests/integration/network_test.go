package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rubberpirate/veaver/internal/platform"
	"github.com/rubberpirate/veaver/internal/transport"
)

func TestNamespacePeer(t *testing.T) {
	if os.Getenv("VEAVER_PEER") != "1" {
		t.Skip("isolated peer helper")
	}
	run := func(args ...string) {
		if b, e := exec.Command("ip", args...).CombinedOutput(); e != nil {
			t.Fatal(string(b), e)
		}
	}
	run("link", "set", "lo", "up")
	run("addr", "add", "198.18.0.1/32", "dev", "lo")
	echo, e := net.Listen("tcp4", "198.18.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = fmt.Fprintln(c, c.RemoteAddr().(*net.TCPAddr).IP.String())
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	udp, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("198.18.0.1"), Port: echo.Addr().(*net.TCPAddr).Port})
	if e != nil {
		t.Fatal(e)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, source, e := udp.ReadFromUDP(buf)
			if e != nil {
				return
			}
			reply := append([]byte(source.IP.String()+"\n"), buf[:n]...)
			_, _ = udp.WriteToUDP(reply, source)
		}
	}()
	pair, e := tls.LoadX509KeyPair(filepath.Join(os.Getenv("VEAVER_CERT_DIR"), "cert.pem"), filepath.Join(os.Getenv("VEAVER_CERT_DIR"), "key.pem"))
	if e != nil {
		t.Fatal(e)
	}
	lc := net.ListenConfig{}
	lc.SetMultipathTCP(true)
	l, e := lc.Listen(context.Background(), "tcp4", "0.0.0.0:0")
	if e != nil {
		t.Fatal(e)
	}
	fmt.Printf("READY %d %d\n", echo.Addr().(*net.TCPAddr).Port, l.Addr().(*net.TCPAddr).Port)
	server := transport.RelayServer{TLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, Token: strings.Repeat("a", 64)}
	if e = server.Serve(context.Background(), l); e != nil {
		t.Fatal(e)
	}
}

func credentials(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	c := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, c, c, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	pk, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	for name, b := range map[string][]byte{"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "key.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), "token": []byte(strings.Repeat("a", 64))} {
		if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return dir
}

func TestRealBalanceAndTransparentRelay(t *testing.T) {
	if os.Getenv("VEAVER_NETNS_TEST") != "1" {
		t.Skip("run scripts/test-netns.sh")
	}
	devices, e := net.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range devices {
		if d.Name != "lo" {
			t.Fatal("refusing packet test outside an empty network namespace")
		}
	}
	ctx := context.Background()
	runner := platform.ExecRunner{}
	run := func(program string, args ...string) {
		t.Helper()
		if _, e := runner.Run(ctx, platform.Command{Program: program, Args: args}); e != nil {
			t.Fatal(e)
		}
	}
	dir := credentials(t)
	peer := exec.Command("unshare", "--net", os.Args[0], "-test.run=^TestNamespacePeer$", "-test.v")
	peer.Env = append(os.Environ(), "VEAVER_PEER=1", "VEAVER_CERT_DIR="+dir)
	stdout, e := peer.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	peer.Stderr = os.Stderr
	if e = peer.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = peer.Process.Kill(); _ = peer.Wait() }()
	scan := bufio.NewScanner(stdout)
	echoPort, relayPort := 0, 0
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "READY ") {
			_, _ = fmt.Sscanf(line, "READY %d %d", &echoPort, &relayPort)
			break
		}
	}
	if echoPort == 0 || relayPort == 0 {
		t.Fatal("peer did not become ready")
	}
	pid := strconv.Itoa(peer.Process.Pid)
	run("ip", "link", "set", "lo", "up")
	links := []platform.Interface{}
	for i, name := range []string{"lwtest0", "lwtest1"} {
		suffix := strconv.Itoa(i + 1)
		other := "lwpeer" + strconv.Itoa(i)
		address := "10.90." + suffix + ".2"
		gateway := "10.90." + suffix + ".1"
		run("ip", "link", "add", name, "type", "veth", "peer", "name", other)
		defer runner.Run(ctx, platform.Command{Program: "ip", Args: []string{"link", "del", name}})
		run("ip", "link", "set", other, "netns", pid)
		run("ip", "addr", "add", address+"/24", "dev", name)
		run("ip", "link", "set", name, "up")
		run("nsenter", "-t", pid, "-n", "ip", "addr", "add", gateway+"/24", "dev", other)
		run("nsenter", "-t", pid, "-n", "ip", "link", "set", other, "up")
		links = append(links, platform.Interface{Name: name, Address: address, Gateway: gateway, Prefix: 24})
	}
	run("ip", "route", "add", "default", "via", links[0].Gateway, "dev", links[0].Name)
	r := platform.NewRouting(filepath.Join(t.TempDir(), "routing.json"), runner)
	defer r.Cleanup(ctx)
	plan, e := platform.RoutePlan("balance", links, 18081)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Apply(ctx, plan); e != nil {
		t.Fatal(e)
	}
	target := fmt.Sprintf("198.18.0.1:%d", echoPort)
	sources := map[string]bool{}
	for i := 0; i < 6; i++ {
		source := roundTrip(t, target, 128<<10)
		sources[source] = true
	}
	if !sources[links[0].Address] || !sources[links[1].Address] {
		t.Fatalf("connections did not use both uplinks: %v", sources)
	}
	// Keep an application socket open while changing weights: its mark and
	// reverse-NAT mapping must survive the atomic scheduler replacement.
	held, e := net.DialTimeout("tcp4", target, 4*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	_ = held.SetDeadline(time.Now().Add(10 * time.Second))
	heldReader := bufio.NewReader(held)
	if _, e = heldReader.ReadString('\n'); e != nil {
		t.Fatal(e)
	}
	plan, e = platform.WeightedRoutePlan("balance", links, 18081, map[string]int{links[0].Name: 3, links[1].Name: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Apply(ctx, plan); e != nil {
		t.Fatal(e)
	}
	if _, e = held.Write([]byte("keep-this-transfer")); e != nil {
		t.Fatal(e)
	}
	if e = held.(*net.TCPConn).CloseWrite(); e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(heldReader)
	if e != nil || string(data) != "keep-this-transfer" {
		t.Fatal("weight update broke established transfer", string(data), e)
	}
	counts := map[string]int{}
	for i := 0; i < 16; i++ {
		counts[roundTrip(t, target, 32<<10)]++
	}
	if counts[links[0].Address] != 12 || counts[links[1].Address] != 4 {
		t.Fatal("unexpected weighted flow distribution", counts)
	}
	var concurrent sync.WaitGroup
	for i := 0; i < 8; i++ {
		concurrent.Add(1)
		go func() { defer concurrent.Done(); roundTrip(t, target, 512<<10) }()
	}
	concurrent.Wait()
	udpSources := map[string]bool{}
	for i := 0; i < 8; i++ {
		c, e := net.DialTimeout("udp4", target, 4*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		_ = c.SetDeadline(time.Now().Add(4 * time.Second))
		payload := []byte("upload/download datagram")
		if _, e = c.Write(payload); e != nil {
			c.Close()
			t.Fatal(e)
		}
		buf := make([]byte, 2048)
		n, e := c.Read(buf)
		c.Close()
		if e != nil {
			t.Fatal(e)
		}
		parts := bytes.SplitN(buf[:n], []byte{'\n'}, 2)
		if len(parts) != 2 || !bytes.Equal(parts[1], payload) {
			t.Fatal("UDP payload differs")
		}
		udpSources[string(parts[0])] = true
	}
	if !udpSources[links[0].Address] || !udpSources[links[1].Address] {
		t.Fatal("UDP did not use both uplinks", udpSources)
	}
	t.Logf("Weighted TCP flows 12:4; established transfer retained; 8 concurrent 512 KiB transfers and UDP echo verified")
	if e = r.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	relay := transport.RelayConfig{Address: fmt.Sprintf("%s:%d", links[0].Gateway, relayPort), ServerName: "127.0.0.1", CAFile: filepath.Join(dir, "cert.pem"), TokenFile: filepath.Join(dir, "token")}
	proxy, e := transport.StartProxy(ctx, relay, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer proxy.Close()
	plan, e = platform.RoutePlan("relay", links, proxy.Port())
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Apply(ctx, plan); e != nil {
		t.Fatal(e)
	}
	if source := roundTrip(t, target, 2<<20); source != "198.18.0.1" {
		t.Fatalf("application did not pass through relay: source=%s", source)
	}
	deadline := time.Now().Add(time.Second)
	for proxy.Metrics.Snapshot().Active > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	m := proxy.Metrics.Snapshot()
	if m.Opened != 1 || m.TX != 2<<20 || m.RX < 2<<20 || m.MPTCP+m.TCPFallback != 1 {
		t.Fatalf("invalid transparent relay metrics: %+v", m)
	}
	if e = r.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	t.Logf("Verified two distinct outbound source addresses and %d-byte upload/download through transparent relay; MPTCP=%d TCP-fallback=%d peak-subflows=%d", m.TX, m.MPTCP, m.TCPFallback, m.MaxSubflows)
}
func roundTrip(t *testing.T, target string, size int) string {
	t.Helper()
	c, e := net.DialTimeout("tcp4", target, 4*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(c)
	source, e := reader.ReadString('\n')
	if e != nil {
		t.Fatal(e)
	}
	payload := bytes.Repeat([]byte("x"), size)
	done := make(chan error, 1)
	go func() {
		_, e := c.Write(payload)
		if e == nil {
			e = c.(*net.TCPConn).CloseWrite()
		}
		done <- e
	}()
	received, e := io.ReadAll(reader)
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("network round-trip payload differs")
	}
	return strings.TrimSpace(source)
}
