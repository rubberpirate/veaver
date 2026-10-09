package transport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRelayDestinationBoundary(t *testing.T) {
	for _, target := range []string{"127.0.0.1:443", "10.0.0.1:443", "[::1]:443", "169.254.169.254:80", "100.64.0.1:80", "example.com:443", "1.1.1.1:0", "1.1.1.1:65536", "1.1.1.1:http"} {
		if e := ValidateTarget(target, false); e == nil {
			t.Errorf("unsafe target accepted: %s", target)
		}
	}
	if e := ValidateTarget("1.1.1.1:443", false); e != nil {
		t.Fatal(e)
	}
}
func TestOriginalDestinationDecoder(t *testing.T) {
	b := make([]byte, 16)
	binary.NativeEndian.PutUint16(b, syscall.AF_INET)
	binary.BigEndian.PutUint16(b[2:], 443)
	copy(b[4:8], net.ParseIP("1.2.3.4").To4())
	s, e := DecodeDestination(b)
	if e != nil || s != "1.2.3.4:443" {
		t.Fatal(s, e)
	}
	if _, e = DecodeDestination(b[:8]); e == nil {
		t.Fatal("short sockaddr accepted")
	}
}
func testTLS(t *testing.T) (*tls.Config, string) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if e = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, path
}
func TestRelayRoundTripAuthenticationAndCancellation(t *testing.T) {
	destination, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer destination.Close()
	go func() {
		for {
			c, e := destination.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	cfg, ca := testTLS(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lc := net.ListenConfig{}
	lc.SetMultipathTCP(true)
	l, e := lc.Listen(ctx, "tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	token := strings.Repeat("a", 64)
	path := filepath.Join(t.TempDir(), "token")
	if e = os.WriteFile(path, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	server := RelayServer{TLS: cfg, Token: token, AllowPrivate: true}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, l) }()
	client := RelayConfig{Address: l.Addr().String(), CAFile: ca, TokenFile: path}
	m := Metrics{}
	c, e := Connect(ctx, client, destination.Addr().String(), false, &m)
	if e != nil {
		t.Fatal(e)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	payload := bytes.Repeat([]byte("linkweave-bidirectional\x00"), 65536)
	writeDone := make(chan error, 1)
	go func() {
		_, e := c.Write(payload)
		if w, ok := c.(interface{ CloseWrite() error }); ok && e == nil {
			e = w.CloseWrite()
		}
		writeDone <- e
	}()
	received, e := io.ReadAll(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = <-writeDone; e != nil {
		t.Fatal(e)
	}
	_ = c.Close()
	if !bytes.Equal(payload, received) {
		t.Fatal("relayed upload/download payload differs")
	}
	if m.Snapshot().MPTCP+m.Snapshot().TCPFallback != 1 {
		t.Fatal("negotiation metrics missing")
	}
	wrong := filepath.Join(t.TempDir(), "wrong")
	_ = os.WriteFile(wrong, []byte(strings.Repeat("b", 64)), 0600)
	bad := client
	bad.TokenFile = wrong
	if c, e = Connect(ctx, bad, destination.Addr().String(), false, &m); e == nil {
		_ = c.Close()
		t.Fatal("wrong relay token accepted")
	}
	untrusted := client
	untrusted.CAFile = ""
	if c, e = Connect(ctx, untrusted, destination.Addr().String(), false, &m); e == nil {
		_ = c.Close()
		t.Fatal("untrusted certificate accepted")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop")
	}
}
