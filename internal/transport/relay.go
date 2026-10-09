package transport

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"linkweave/internal/platform"
)

type RelayConfig struct {
	Address    string `json:"address"`
	ServerName string `json:"server_name"`
	CAFile     string `json:"ca_file,omitempty"`
	TokenFile  string `json:"token_file,omitempty"`
}

func (c RelayConfig) Validate() error {
	host, port, e := net.SplitHostPort(c.Address)
	if e != nil || host == "" || port == "" {
		return errors.New("relay address must be host:port")
	}
	if strings.ContainsAny(c.ServerName, "\r\n\x00") {
		return errors.New("invalid TLS server name")
	}
	if c.TokenFile == "" {
		return errors.New("relay token file is required")
	}
	return nil
}
func ReadToken(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 32 || len(s) > 256 || strings.ContainsAny(s, " \t\r\n") {
		return "", errors.New("token must contain 32–256 non-whitespace characters")
	}
	return s, nil
}
func ClientTLS(c RelayConfig) (*tls.Config, error) {
	name := c.ServerName
	if name == "" {
		name, _, _ = net.SplitHostPort(c.Address)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: name}
	if c.CAFile != "" {
		b, e := os.ReadFile(c.CAFile)
		if e != nil {
			return nil, e
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("relay CA file has no certificates")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

type Metrics struct {
	Active      atomic.Int64
	Opened      atomic.Uint64
	RX          atomic.Uint64
	TX          atomic.Uint64
	MPTCP       atomic.Uint64
	TCPFallback atomic.Uint64
	Failures    atomic.Uint64
	MaxSubflows atomic.Uint64
}
type Snapshot struct {
	Active      int64  `json:"active"`
	Opened      uint64 `json:"opened"`
	RX          uint64 `json:"rx_bytes"`
	TX          uint64 `json:"tx_bytes"`
	MPTCP       uint64 `json:"mptcp_connections"`
	TCPFallback uint64 `json:"tcp_fallback_connections"`
	Failures    uint64 `json:"failures"`
	MaxSubflows uint64 `json:"max_observed_subflows"`
}

func (m *Metrics) Snapshot() Snapshot {
	return Snapshot{m.Active.Load(), m.Opened.Load(), m.RX.Load(), m.TX.Load(), m.MPTCP.Load(), m.TCPFallback.Load(), m.Failures.Load(), m.MaxSubflows.Load()}
}

type countedReader struct {
	r io.Reader
	n *atomic.Uint64
}

func (r countedReader) Read(b []byte) (int, error) {
	n, e := r.r.Read(b)
	r.n.Add(uint64(n))
	return n, e
}

type bufferedConn struct {
	net.Conn
	r   *bufio.Reader
	tcp *net.TCPConn
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
func (c *bufferedConn) CloseWrite() error {
	if w, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return c.Conn.Close()
}
func pipe(ctx context.Context, a, b net.Conn, m *Metrics) {
	done := make(chan struct{})
	observe := func() {
		for _, c := range []net.Conn{a, b} {
			if wrapped, ok := c.(*bufferedConn); ok {
				if count, known := subflows(wrapped.tcp); known {
					for old := m.MaxSubflows.Load(); uint64(count) > old; old = m.MaxSubflows.Load() {
						if m.MaxSubflows.CompareAndSwap(old, uint64(count)) {
							break
						}
					}
				}
			}
		}
	}
	observe()
	defer observe()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = a.Close()
				_ = b.Close()
				return
			case <-done:
				return
			case <-ticker.C:
				observe()
			}
		}
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	copyHalf := func(dst, src net.Conn, n *atomic.Uint64) {
		defer wg.Done()
		_, err := io.Copy(dst, countedReader{src, n})
		if err != nil {
			_ = a.Close()
			_ = b.Close()
			return
		}
		if w, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = w.CloseWrite()
		}
	}
	go copyHalf(b, a, &m.TX)
	go copyHalf(a, b, &m.RX)
	wg.Wait()
	close(done)
}

func Connect(ctx context.Context, c RelayConfig, target string, bypass bool, m *Metrics) (net.Conn, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	token, e := ReadToken(c.TokenFile)
	if e != nil {
		return nil, e
	}
	cfg, e := ClientTLS(c)
	if e != nil {
		return nil, e
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	dialer.SetMultipathTCP(true)
	if bypass {
		dialer.Control = func(network, address string, raw syscall.RawConn) error {
			var e error
			ce := raw.Control(func(fd uintptr) {
				e = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, platform.BypassMark)
			})
			return errors.Join(ce, e)
		}
	}
	raw, e := dialer.DialContext(ctx, "tcp", c.Address)
	if e != nil {
		return nil, e
	}
	success := false
	defer func() {
		if !success {
			_ = raw.Close()
		}
	}()
	multipath := false
	if tcp, ok := raw.(*net.TCPConn); ok {
		multipath, _ = tcp.MultipathTCP()
	}
	conn := tls.Client(raw, cfg)
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if e = conn.HandshakeContext(ctx); e != nil {
		return nil, e
	}
	req, e := http.NewRequest(http.MethodConnect, "http://"+target, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if e = req.Write(conn); e != nil {
		return nil, e
	}
	reader := bufio.NewReaderSize(conn, 4096)
	resp, e := http.ReadResponse(reader, req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("relay rejected connection: HTTP %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	if multipath {
		m.MPTCP.Add(1)
	} else {
		m.TCPFallback.Add(1)
	}
	success = true
	tcp, _ := raw.(*net.TCPConn)
	return &bufferedConn{Conn: conn, r: reader, tcp: tcp}, nil
}

type Proxy struct {
	Listener net.Listener
	Config   RelayConfig
	Metrics  *Metrics
	Log      func(string, string)
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	slots    chan struct{}
}

func StartProxy(parent context.Context, c RelayConfig, log func(string, string)) (*Proxy, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if _, e := ReadToken(c.TokenFile); e != nil {
		return nil, e
	}
	if _, e := ClientTLS(c); e != nil {
		return nil, e
	}
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(parent)
	p := &Proxy{Listener: l, Config: c, Metrics: &Metrics{}, Log: log, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 1024)}
	p.wg.Add(1)
	go p.accept()
	return p, nil
}
func (p *Proxy) Port() int { return p.Listener.Addr().(*net.TCPAddr).Port }
func (p *Proxy) Close()    { p.cancel(); _ = p.Listener.Close(); p.wg.Wait() }
func (p *Proxy) accept() {
	defer p.wg.Done()
	for {
		c, e := p.Listener.Accept()
		if e != nil {
			return
		}
		select {
		case p.slots <- struct{}{}:
			p.wg.Add(1)
			go p.handle(c)
		default:
			_ = c.Close()
		}
	}
}
func (p *Proxy) handle(c net.Conn) {
	defer p.wg.Done()
	defer func() { <-p.slots }()
	defer c.Close()
	target, e := OriginalDestination(c.(*net.TCPConn))
	if e != nil {
		p.fail("original destination unavailable")
		return
	}
	remote, e := Connect(p.ctx, p.Config, target, true, p.Metrics)
	if e != nil {
		p.fail(e.Error())
		return
	}
	defer remote.Close()
	p.Metrics.Opened.Add(1)
	p.Metrics.Active.Add(1)
	defer p.Metrics.Active.Add(-1)
	pipe(p.ctx, c, remote, p.Metrics)
}
func (p *Proxy) fail(s string) {
	p.Metrics.Failures.Add(1)
	if p.Log != nil {
		p.Log("warning", "Relay: "+s)
	}
}

type RelayServer struct {
	TLS            *tls.Config
	Token          string
	MaxConnections int
	AllowPrivate   bool
	Metrics        Metrics
	Log            func(string, string)
}

func (s *RelayServer) Serve(ctx context.Context, l net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if s.TLS == nil || len(s.Token) < 32 {
		return errors.New("relay requires TLS credentials and a strong token")
	}
	limit := s.MaxConnections
	if limit == 0 {
		limit = 1024
	}
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = l.Close()
		case <-done:
		}
	}()
	for {
		raw, e := l.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		select {
		case slots <- struct{}{}:
			wg.Add(1)
			go func() { defer wg.Done(); defer func() { <-slots }(); s.handle(ctx, raw) }()
		default:
			_ = raw.Close()
		}
	}
}
func (s *RelayServer) handle(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn := tls.Server(raw, s.TLS)
	if e := conn.HandshakeContext(ctx); e != nil {
		s.Metrics.Failures.Add(1)
		return
	}
	limited := &io.LimitedReader{R: conn, N: 8192}
	reader := bufio.NewReaderSize(limited, 8192)
	req, e := http.ReadRequest(reader)
	if e != nil {
		return
	}
	auth := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if req.Method != http.MethodConnect || len(req.TransferEncoding) > 0 || req.ContentLength > 0 || subtle.ConstantTimeCompare([]byte(auth), []byte(s.Token)) != 1 {
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	target := req.Host
	if e = ValidateTarget(target, s.AllowPrivate); e != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	remote, e := d.DialContext(ctx, "tcp", target)
	if e != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer remote.Close()
	if _, e = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	s.Metrics.Active.Add(1)
	s.Metrics.Opened.Add(1)
	defer s.Metrics.Active.Add(-1)
	limited.N = 1 << 62
	tcp, _ := raw.(*net.TCPConn)
	pipe(ctx, &bufferedConn{Conn: conn, r: reader, tcp: tcp}, remote, &s.Metrics)
}
func ValidateTarget(target string, allowPrivate bool) error {
	host, port, e := net.SplitHostPort(target)
	if e != nil {
		return errors.New("target must be IP:port")
	}
	addr, e := netip.ParseAddr(host)
	if e != nil {
		return errors.New("only literal destination addresses are permitted")
	}
	addr = addr.Unmap()
	n, parseErr := strconv.Atoi(port)
	if parseErr != nil || n < 1 || n > 65535 {
		return errors.New("invalid target port")
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return errors.New("target port must be numeric")
		}
	}
	if !allowPrivate && (!addr.IsGlobalUnicast() || addr.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(addr)) {
		return errors.New("private/non-unicast destinations are blocked")
	}
	return nil
}
