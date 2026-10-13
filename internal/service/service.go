package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rubberpirate/veaver/internal/platform"
	"github.com/rubberpirate/veaver/internal/transport"
)

type Config struct {
	Mode         string                `json:"mode"`
	Interfaces   []string              `json:"interfaces"`
	AllowMetered bool                  `json:"allow_metered"`
	Weights      map[string]int        `json:"weights,omitempty"`
	Relay        transport.RelayConfig `json:"relay"`
}

func DefaultConfig() Config { return Config{Mode: "observe", Interfaces: []string{}} }
func (c Config) Validate() error {
	if c.Mode != "observe" && c.Mode != "balance" && c.Mode != "relay" {
		return errors.New("mode must be observe, balance or relay")
	}
	seen := map[string]bool{}
	for _, name := range c.Interfaces {
		if !platform.ValidInterface(name) || seen[name] {
			return errors.New("invalid or duplicate interface")
		}
		seen[name] = true
	}
	if len(c.Interfaces) > 16 {
		return errors.New("at most 16 interfaces are supported")
	}
	if len(c.Weights) > 16 {
		return errors.New("at most 16 link weights are supported")
	}
	for name, weight := range c.Weights {
		if !platform.ValidInterface(name) || weight < 1 || weight > 100 {
			return errors.New("weights require valid interface names and integers from 1 to 100")
		}
	}
	if c.Mode == "relay" {
		return c.Relay.Validate()
	}
	return nil
}
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&c); e != nil {
		return c, e
	}
	return c, c.Validate()
}

type Event struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}
type Sample struct {
	Time time.Time `json:"time"`
	RX   float64   `json:"rx"`
	TX   float64   `json:"tx"`
}
type Snapshot struct {
	Time       time.Time          `json:"time"`
	Started    time.Time          `json:"started"`
	Version    string             `json:"version"`
	Network    platform.Network   `json:"network"`
	Config     Config             `json:"config"`
	ActiveMode string             `json:"active_mode"`
	LastError  string             `json:"last_error,omitempty"`
	Checks     []platform.Check   `json:"checks"`
	RXRate     float64            `json:"rx_rate"`
	TXRate     float64            `json:"tx_rate"`
	RXBytes    uint64             `json:"rx_bytes"`
	TXBytes    uint64             `json:"tx_bytes"`
	History    []Sample           `json:"history"`
	Events     []Event            `json:"events"`
	Relay      transport.Snapshot `json:"relay"`
}
type Monitor interface {
	Snapshot(context.Context) (platform.Network, error)
	Close()
}
type Service struct {
	mu           sync.RWMutex
	op           sync.Mutex
	cfg          Config
	snapshot     Snapshot
	configPath   string
	nm           Monitor
	routing      *platform.Routing
	proxy        *transport.Proxy
	ctx          context.Context
	cancel       context.CancelFunc
	wake         chan struct{}
	done         chan struct{}
	previous     map[string]platform.Interface
	previousTime time.Time
	signature    string
	nextRetry    time.Time
	lock         *os.File
}

func New(configPath, stateDir string, nm Monitor, runner platform.Runner) (*Service, error) {
	cfg, e := LoadConfig(configPath)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(stateDir, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(stateDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = f.Close()
		return nil, errors.New("another Veaver daemon is using this state directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{cfg: cfg, configPath: configPath, nm: nm, routing: platform.NewRouting(filepath.Join(stateDir, "routing.json"), runner), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), previous: map[string]platform.Interface{}, lock: f}
	s.snapshot = Snapshot{Started: time.Now(), Version: "0.2.0", Config: cfg, ActiveMode: "observe", Network: platform.Network{Interfaces: []platform.Interface{}}, History: []Sample{}, Events: []Event{}, Checks: []platform.Check{}}
	if e = s.routing.Load(); e != nil {
		s.release()
		return nil, e
	}
	if e = s.routing.Cleanup(ctx); e != nil {
		s.release()
		return nil, fmt.Errorf("routing recovery: %w", e)
	}
	s.Log("info", "service", "Veaver started; NetworkManager owns physical connectivity")
	go s.loop()
	return s, nil
}
func (s *Service) release() {
	s.cancel()
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	_ = s.lock.Close()
}
func (s *Service) Close() error {
	s.cancel()
	<-s.done
	s.op.Lock()
	defer s.op.Unlock()
	e := s.routing.Cleanup(context.Background())
	if s.proxy != nil {
		s.proxy.Close()
		s.proxy = nil
	}
	s.nm.Close()
	s.release()
	return e
}
func (s *Service) Log(level, source, message string) {
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\x1b", ""), "\x00", "")
	if len(message) > 2000 {
		message = message[:2000]
	}
	event := Event{time.Now(), level, source, message}
	s.mu.Lock()
	s.snapshot.Events = append(s.snapshot.Events, event)
	if len(s.snapshot.Events) > 200 {
		s.snapshot.Events = s.snapshot.Events[len(s.snapshot.Events)-200:]
	}
	s.mu.Unlock()
	fmt.Fprintf(os.Stderr, "%s %s [%s] %s\n", event.Time.Format(time.RFC3339), level, source, message)
}
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.snapshot
	out.Network.Interfaces = append([]platform.Interface{}, out.Network.Interfaces...)
	out.History = append([]Sample{}, out.History...)
	out.Events = append([]Event{}, out.Events...)
	out.Checks = append([]platform.Check{}, out.Checks...)
	out.Config.Interfaces = append([]string{}, out.Config.Interfaces...)
	out.Config.Weights = maps.Clone(out.Config.Weights)
	return out
}
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) loop() {
	defer close(s.done)
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	s.refresh()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.refresh()
		case <-s.wake:
			s.refresh()
		}
	}
}
func selected(c Config, n platform.Network) ([]platform.Interface, error) {
	wanted := map[string]bool{}
	for _, name := range c.Interfaces {
		wanted[name] = true
	}
	var links []platform.Interface
	for _, d := range n.Interfaces {
		if len(c.Interfaces) > 0 && !wanted[d.Name] {
			continue
		}
		if platform.Eligible(d, c.AllowMetered) {
			links = append(links, d)
		}
	}
	if len(links) == 0 {
		return nil, errors.New("no eligible uplinks available")
	}
	return links, nil
}
func (s *Service) Plan(c Config) (platform.Plan, error) {
	if e := c.Validate(); e != nil {
		return platform.Plan{}, e
	}
	if c.Mode == "observe" {
		return platform.Plan{Mode: "observe", Commands: []platform.Command{}, Interfaces: []string{}, Warnings: []string{"Restore Veaver-owned rules; preserve NetworkManager configuration"}}, nil
	}
	snap := s.Snapshot()
	if snap.Network.Version == "" {
		return platform.Plan{}, errors.New("NetworkManager is unavailable")
	}
	if snap.Network.VPNActive {
		return platform.Plan{}, errors.New("disable the existing NetworkManager VPN before changing routing")
	}
	links, e := selected(c, snap.Network)
	if e != nil {
		return platform.Plan{}, e
	}
	port := 18081
	s.op.Lock()
	if s.proxy != nil {
		port = s.proxy.Port()
	}
	s.op.Unlock()
	return platform.WeightedRoutePlan(c.Mode, links, port, c.Weights)
}
func (s *Service) Update(c Config) error {
	c.Interfaces = append([]string{}, c.Interfaces...)
	c.Weights = maps.Clone(c.Weights)
	if e := c.Validate(); e != nil {
		return e
	}
	s.op.Lock()
	defer s.op.Unlock()
	snap := s.Snapshot()
	old := snap.Config
	if e := s.activate(c, snap.Network); e != nil {
		restore := s.activate(old, snap.Network)
		return errors.Join(e, restore)
	}
	if e := platform.AtomicJSON(s.configPath, c, 0600); e != nil {
		restore := s.activate(old, snap.Network)
		return errors.Join(e, restore)
	}
	s.mu.Lock()
	s.cfg = c
	s.snapshot.Config = c
	s.snapshot.LastError = ""
	s.mu.Unlock()
	s.signature = networkSignature(c, snap.Network, "")
	s.nextRetry = time.Time{}
	s.Log("info", "policy", "Mode changed to "+c.Mode)
	s.Wake()
	return nil
}
func (s *Service) activate(c Config, n platform.Network) error {
	if c.Mode == "observe" {
		if e := s.routing.Cleanup(s.ctx); e != nil {
			return e
		}
		if s.proxy != nil {
			s.proxy.Close()
			s.proxy = nil
		}
		s.mu.Lock()
		s.snapshot.ActiveMode = "observe"
		s.mu.Unlock()
		return nil
	}
	if n.VPNActive {
		return errors.New("an existing NetworkManager VPN is active; refusing to bypass it")
	}
	links, e := selected(c, n)
	if e != nil {
		return e
	}
	var fresh *transport.Proxy
	port := 18081
	reuse := c.Mode == "relay" && s.proxy != nil && s.proxy.Config == c.Relay
	if reuse {
		port = s.proxy.Port()
	} else if c.Mode == "relay" {
		fresh, e = transport.StartProxy(s.ctx, c.Relay, func(level, message string) { s.Log(level, "relay", message) })
		if e != nil {
			return e
		}
		port = fresh.Port()
		// Authenticate an actual connection before installing interception rules.
		target := netPublicProbeTarget()
		probeCtx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		probe, e := transport.Connect(probeCtx, c.Relay, target, true, fresh.Metrics)
		cancel()
		if e != nil {
			fresh.Close()
			return fmt.Errorf("relay preflight: %w", e)
		}
		_ = probe.Close()
	}
	p, e := platform.WeightedRoutePlan(c.Mode, links, port, c.Weights)
	if e != nil {
		if fresh != nil {
			fresh.Close()
		}
		return e
	}
	if e = s.routing.Apply(s.ctx, p); e != nil {
		if fresh != nil {
			fresh.Close()
		}
		s.mu.Lock()
		s.snapshot.ActiveMode = "observe"
		s.mu.Unlock()
		return e
	}
	if s.proxy != nil && !reuse {
		s.proxy.Close()
	}
	if !reuse {
		s.proxy = fresh
	}
	s.mu.Lock()
	s.snapshot.ActiveMode = c.Mode
	s.mu.Unlock()
	return nil
}
func netPublicProbeTarget() string { return "1.1.1.1:443" }
func (s *Service) refresh() {
	ctx, cancel := context.WithTimeout(s.ctx, 6*time.Second)
	n, e := s.nm.Snapshot(ctx)
	cancel()
	if s.ctx.Err() != nil {
		return
	}
	now := time.Now()
	nmError := ""
	if e != nil {
		nmError = e.Error()
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.RLock()
	cfg := s.cfg
	previousNetwork := s.snapshot.Network
	s.mu.RUnlock()
	present := map[string]bool{}
	for i := range n.Interfaces {
		d := &n.Interfaces[i]
		present[d.Name] = true
		if old, ok := s.previous[d.Name]; ok && !s.previousTime.IsZero() {
			dt := now.Sub(s.previousTime).Seconds()
			if dt > 0 && d.RXBytes >= old.RXBytes {
				d.RXRate = float64(d.RXBytes-old.RXBytes) / dt
			}
			if dt > 0 && d.TXBytes >= old.TXBytes {
				d.TXRate = float64(d.TXBytes-old.TXBytes) / dt
			}
		}
		s.previous[d.Name] = *d
		if len(cfg.Interfaces) == 0 {
			d.Selected = platform.Eligible(*d, cfg.AllowMetered)
		} else {
			for _, name := range cfg.Interfaces {
				if d.Name == name {
					d.Selected = true
				}
			}
		}
	}
	for name := range s.previous {
		if !present[name] {
			delete(s.previous, name)
		}
	}
	s.previousTime = now
	sig := networkSignature(cfg, n, nmError)
	if cfg.Mode != "observe" && (sig != s.signature || retryDue(now, s.nextRetry)) {
		s.signature = sig
		if e = s.activate(cfg, n); e != nil {
			s.nextRetry = now.Add(30 * time.Second)
			clean := s.routing.Cleanup(context.Background())
			if s.proxy != nil {
				s.proxy.Close()
				s.proxy = nil
			}
			s.mu.Lock()
			s.snapshot.ActiveMode = "observe"
			s.snapshot.LastError = errors.Join(e, clean).Error()
			s.mu.Unlock()
			s.Log("warning", "policy", "Routing suspended: "+e.Error())
		} else {
			s.nextRetry = time.Time{}
			s.mu.Lock()
			s.snapshot.LastError = ""
			s.mu.Unlock()
			s.Log("info", "policy", "Routing reconciled with NetworkManager uplinks")
		}
	}
	if nmError != "" {
		s.mu.Lock()
		s.snapshot.LastError = nmError
		s.mu.Unlock()
	} else if cfg.Mode == "observe" {
		s.mu.Lock()
		s.snapshot.LastError = ""
		s.mu.Unlock()
	}
	if previousNetwork.State != n.State && n.State != "" {
		s.Log("info", "networkmanager", "Connectivity state: "+n.State)
	}
	if !reflect.DeepEqual(interfaceStates(previousNetwork), interfaceStates(n)) {
		s.Log("info", "networkmanager", "Interfaces or addresses changed")
	}
	var rx, tx float64
	var rxb, txb uint64
	for _, d := range n.Interfaces {
		if d.Physical {
			rx += d.RXRate
			tx += d.TXRate
			rxb += d.RXBytes
			txb += d.TXBytes
		}
	}
	s.mu.Lock()
	s.snapshot.Time = now
	s.snapshot.Network = n
	s.snapshot.Checks = platform.Checks(n, nmError)
	for i, warning := range s.routing.Warnings {
		s.snapshot.Checks = append(s.snapshot.Checks, platform.Check{ID: fmt.Sprintf("mptcp-policy-%d", i), Name: "MPTCP endpoint configuration", Status: "warning", Detail: warning})
	}
	s.snapshot.RXRate = rx
	s.snapshot.TXRate = tx
	s.snapshot.RXBytes = rxb
	s.snapshot.TXBytes = txb
	s.snapshot.History = append(s.snapshot.History, Sample{now, rx, tx})
	if len(s.snapshot.History) > 90 {
		s.snapshot.History = s.snapshot.History[len(s.snapshot.History)-90:]
	}
	if s.proxy != nil {
		s.snapshot.Relay = s.proxy.Metrics.Snapshot()
	} else {
		s.snapshot.Relay = transport.Snapshot{}
	}
	s.mu.Unlock()
}
func interfaceStates(n platform.Network) map[string]string {
	m := map[string]string{}
	for _, d := range n.Interfaces {
		m[d.Name] = d.State + ":" + d.Address
	}
	return m
}

func networkSignature(c Config, n platform.Network, err string) string {
	parts := []string{platform.JSON(c), fmt.Sprint(n.VPNActive), err}
	links, _ := selected(c, n)
	for _, d := range links {
		parts = append(parts, fmt.Sprintf("%s:%s:%s:%d", d.Name, d.Address, d.Gateway, d.Prefix))
	}
	return strings.Join(parts, "|")
}

func retryDue(now, next time.Time) bool { return !next.IsZero() && !now.Before(next) }
