package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rubberpirate/veaver/internal/platform"
)

type mockNM struct {
	mu      sync.Mutex
	network platform.Network
}

func (n *mockNM) Snapshot(context.Context) (platform.Network, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	x := n.network
	x.Interfaces = append([]platform.Interface{}, x.Interfaces...)
	return x, nil
}
func (n *mockNM) Close() {}

type noRun struct{ t *testing.T }

func (r noRun) Run(context.Context, platform.Command) (string, error) {
	r.t.Error("observe mode invoked a network command")
	return "", nil
}
func TestObserveServiceAndConfigIsolation(t *testing.T) {
	dir := t.TempDir()
	n := &mockNM{network: platform.Network{Version: "1.20", State: "connected", Interfaces: []platform.Interface{{Name: "eth0", Physical: true, Managed: true, Connected: true, Address: "192.168.1.2", Gateway: "192.168.1.1", Prefix: 24, RXBytes: 1000}}}}
	s, e := New(filepath.Join(dir, "config.json"), filepath.Join(dir, "state"), n, noRun{t})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().Time.IsZero() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snap := s.Snapshot()
	if snap.Network.Version != "1.20" || snap.ActiveMode != "observe" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	snap.Network.Interfaces[0].Name = "modified"
	if s.Snapshot().Network.Interfaces[0].Name == "modified" {
		t.Fatal("snapshot leaked mutable state")
	}
	if e = s.Update(DefaultConfig()); e != nil {
		t.Fatal(e)
	}
	cfg, e := LoadConfig(filepath.Join(dir, "config.json"))
	if e != nil || cfg.Mode != "observe" {
		t.Fatal(cfg, e)
	}
	if e = s.Update(Config{Mode: "invalid"}); e == nil {
		t.Fatal("invalid config accepted")
	}
	if _, e := os.Stat(filepath.Join(dir, "state", "routing.json")); !os.IsNotExist(e) {
		t.Fatal("observe created routing state")
	}
}
func TestHotplugSelection(t *testing.T) {
	n := platform.Network{Interfaces: []platform.Interface{{Name: "eth0", Physical: true, Managed: true, Connected: true, Address: "192.168.1.2", Gateway: "192.168.1.1"}, {Name: "usb0", Physical: true, Managed: true, Connected: false}, {Name: "wlan0", Physical: true, Managed: true, Connected: true, Address: "192.168.2.2", Gateway: "192.168.2.1"}}}
	c := Config{Mode: "balance", Interfaces: []string{"eth0", "usb0"}}
	links, e := selected(c, n)
	if e != nil || len(links) != 1 || links[0].Name != "eth0" {
		t.Fatal("selection did not survive unplug", links, e)
	}
	n.Interfaces[0].Metered = true
	if _, e = selected(c, n); e == nil {
		t.Fatal("metered uplink used without opt-in")
	}
}

func TestConnectivityAndWeightIsolation(t *testing.T) {
	d := platform.Interface{Name: "eth0", Physical: true, Managed: true, Connected: true, Address: "192.168.1.2", Gateway: "192.168.1.1"}
	for _, health := range []string{"none", "portal", "limited"} {
		d.Connectivity = health
		if platform.Eligible(d, true) {
			t.Fatal("unhealthy link eligible", health)
		}
	}
	d.Connectivity = "unknown"
	if !platform.Eligible(d, false) {
		t.Fatal("disabled NM connectivity check blocked link")
	}
	c := Config{Mode: "balance", Weights: map[string]int{"eth0": 3}}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	s := Service{snapshot: Snapshot{Config: c}}
	snap := s.Snapshot()
	snap.Config.Weights["eth0"] = 99
	if s.Snapshot().Config.Weights["eth0"] != 3 {
		t.Fatal("snapshot map leaked")
	}
	c.Weights["eth0"] = 101
	if e := c.Validate(); e == nil {
		t.Fatal("invalid weight accepted")
	}
}

type recoveringRunner struct {
	fail     bool
	nft      bool
	attempts int
}

func (r *recoveringRunner) Run(_ context.Context, c platform.Command) (string, error) {
	args := strings.Join(c.Args, " ")
	if c.Program == "nft" {
		if strings.HasPrefix(args, "list table") {
			if !r.nft {
				return "", errors.New("missing")
			}
			return "table", nil
		}
		if args == "-c -f -" {
			r.attempts++
			if r.fail {
				return "", errors.New("temporary failure")
			}
		}
		if args == "-f -" {
			r.nft = true
		}
		if strings.HasPrefix(args, "delete table") {
			r.nft = false
		}
		return "", nil
	}
	return "[]", nil
}
func TestPolicyRetriesWithoutNetworkChange(t *testing.T) {
	dir := t.TempDir()
	d := platform.Interface{Name: "eth0", Physical: true, Managed: true, Connected: true, Address: "192.168.1.2", Gateway: "192.168.1.1", Prefix: 24}
	runner := &recoveringRunner{fail: true}
	routing := platform.NewRouting(filepath.Join(dir, "routing.json"), runner)
	routing.ProcRoot = filepath.Join(dir, "proc")
	if e := os.MkdirAll(filepath.Join(routing.ProcRoot, d.Name), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(routing.ProcRoot, d.Name, "rp_filter"), []byte("1"), 0600); e != nil {
		t.Fatal(e)
	}
	c := Config{Mode: "balance"}
	s := &Service{ctx: context.Background(), cfg: c, snapshot: Snapshot{Config: c, ActiveMode: "observe"}, nm: &mockNM{network: platform.Network{Version: "1.20", Interfaces: []platform.Interface{d}}}, routing: routing, previous: map[string]platform.Interface{}}
	s.refresh()
	if s.nextRetry.IsZero() || s.Snapshot().LastError == "" {
		t.Fatal("failure not scheduled for retry")
	}
	runner.fail = false
	s.refresh()
	if runner.attempts != 1 {
		t.Fatal("retried too early")
	}
	s.nextRetry = time.Now().Add(-time.Second)
	s.refresh()
	if s.Snapshot().ActiveMode != "balance" || s.Snapshot().LastError != "" || !s.nextRetry.IsZero() {
		t.Fatal("unchanged network did not recover", s.Snapshot())
	}
	if e := routing.Cleanup(context.Background()); e != nil {
		t.Fatal(e)
	}
}
