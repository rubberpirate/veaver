package platform

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func testLinks() []Interface {
	return []Interface{{Name: "eth0", Address: "192.168.1.2", Gateway: "192.168.1.1", Prefix: 24}, {Name: "usb0", Address: "172.20.10.2", Gateway: "172.20.10.1", Prefix: 28}}
}
func TestPlanPinsConnectionsAndPreservesBypass(t *testing.T) {
	p, e := RoutePlan("balance", testLinks(), 18081)
	if e != nil {
		t.Fatal(e)
	}
	for _, fragment := range []string{"type route hook output", "ct state new", "ct mark set numgen inc mod 2", "meta mark set ct mark", "ip daddr @bypass return", "snat to 192.168.1.2", "snat to 172.20.10.2", "tcp option maxseg size set rt mtu"} {
		if !strings.Contains(p.NFT, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	other, e := RoutePlan("balance", []Interface{testLinks()[1]}, 18081)
	if e != nil {
		t.Fatal(e)
	}
	if p.Leases[1].Mark != other.Leases[0].Mark {
		t.Fatal("marks changed when one link disappeared")
	}
	relay, e := RoutePlan("relay", testLinks(), 18081)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(relay.NFT, "meta mark != 0 return") || !strings.Contains(relay.NFT, "redirect to :18081") {
		t.Fatal("missing loop bypass or redirect")
	}
	for _, name := range []string{"../eth0", "eth0\"; flush ruleset", "eth0\n"} {
		links := testLinks()
		links[0].Name = name
		if _, e := RoutePlan("balance", links, 18081); e == nil {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
}

func TestWeightedUpdatePreservesRoutes(t *testing.T) {
	f := &fakeRunner{}
	r := setupRouting(t, f)
	p, e := RoutePlan("balance", testLinks(), 18081)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Apply(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	f.calls = nil
	p, e = WeightedRoutePlan("balance", testLinks(), 18081, map[string]int{"eth0": 3, "usb0": 1})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(p.NFT, "numgen inc mod 4") {
		t.Fatal("weight missing")
	}
	if e = r.Apply(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if len(f.calls) != 2 {
		t.Fatalf("updated routes during weight change: %+v", f.calls)
	}
	for _, c := range f.calls {
		if c.Program != "nft" || !strings.HasPrefix(c.Input, "delete table ip veaver\n") {
			t.Fatal("nontransactional scheduler update", c)
		}
	}
	if e = r.Cleanup(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = WeightedRoutePlan("balance", testLinks(), 18081, map[string]int{"eth0": 0}); e == nil {
		t.Fatal("invalid weight accepted")
	}
}

type fakeRunner struct {
	calls     []Command
	rules     []map[string]any
	nft       bool
	failRule  bool
	failed    bool
	collision bool
	endpoints []map[string]any
}

func (f *fakeRunner) Run(_ context.Context, c Command) (string, error) {
	f.calls = append(f.calls, c)
	a := strings.Join(c.Args, " ")
	if a == "-j mptcp endpoint show" {
		if f.endpoints == nil {
			return "[]", nil
		}
		b, _ := json.Marshal(f.endpoints)
		return string(b), nil
	}
	if strings.HasPrefix(a, "mptcp endpoint add") {
		id, _ := strconv.Atoi(c.Args[8])
		f.endpoints = append(f.endpoints, map[string]any{"id": id, "address": c.Args[3]})
		return "", nil
	}
	if strings.HasPrefix(a, "mptcp endpoint delete") {
		id, _ := strconv.Atoi(c.Args[4])
		var keep []map[string]any
		for _, ep := range f.endpoints {
			if ep["id"] != id {
				keep = append(keep, ep)
			}
		}
		f.endpoints = keep
		return "", nil
	}
	if c.Program == "nft" {
		switch {
		case strings.HasPrefix(a, "list table"):
			if f.nft || f.collision {
				return "table", nil
			}
			return "", errors.New("No such file or directory")
		case strings.HasPrefix(a, "delete table"):
			f.nft = false
			return "", nil
		case a == "-f -":
			f.nft = true
			return "", nil
		default:
			return "", nil
		}
	}
	if a == "-j -4 rule show" {
		b, _ := json.Marshal(f.rules)
		if f.rules == nil {
			return "[]", nil
		}
		return string(b), nil
	}
	if strings.HasPrefix(a, "-j -4 route show") {
		return "[]", nil
	}
	if strings.Contains(a, "rule add") {
		if f.failRule && !f.failed {
			f.failed = true
			return "", errors.New("injected routing failure")
		}
		var priority int
		for i, v := range c.Args {
			if v == "priority" {
				priority, _ = strconv.Atoi(c.Args[i+1])
			}
		}
		f.rules = append(f.rules, map[string]any{"priority": priority})
		return "", nil
	}
	if strings.Contains(a, "rule del") {
		f.rules = nil
	}
	return "", nil
}

func TestRelayPreservesForeignEndpoints(t *testing.T) {
	f := &fakeRunner{endpoints: []map[string]any{{"id": 3, "address": testLinks()[0].Address}}}
	r := setupRouting(t, f)
	p, e := RoutePlan("relay", testLinks(), 18081)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Apply(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if len(f.endpoints) != 2 {
		t.Fatal("missing client endpoint")
	}
	remaining, e := RoutePlan("relay", []Interface{testLinks()[1]}, 18081)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Apply(context.Background(), remaining); e != nil {
		t.Fatal(e)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c.Args, " "), "mptcp endpoint delete") {
			t.Fatal("surviving endpoint removed during reconciliation")
		}
	}
	if e = r.Cleanup(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.endpoints) != 1 || f.endpoints[0]["id"] != 3 {
		t.Fatal("foreign MPTCP endpoint was changed")
	}
}
func setupRouting(t *testing.T, f *fakeRunner) *Routing {
	t.Helper()
	dir := t.TempDir()
	r := NewRouting(filepath.Join(dir, "routing.json"), f)
	r.ProcRoot = filepath.Join(dir, "proc")
	for _, d := range testLinks() {
		path := filepath.Join(r.ProcRoot, d.Name)
		if e := os.MkdirAll(path, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(path, "rp_filter"), []byte("1\n"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	return r
}
func TestRoutingRollbackAndRecovery(t *testing.T) {
	ctx := context.Background()
	p, e := RoutePlan("balance", testLinks(), 18081)
	if e != nil {
		t.Fatal(e)
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "rollback"}[fail], func(t *testing.T) {
			f := &fakeRunner{failRule: fail}
			r := setupRouting(t, f)
			e := r.Apply(ctx, p)
			if fail {
				if e == nil {
					t.Fatal("failure not propagated")
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				if !f.nft {
					t.Fatal("rules not installed")
				}
				r2 := NewRouting(r.StatePath, f)
				r2.ProcRoot = r.ProcRoot
				if e = r2.Load(); e != nil {
					t.Fatal(e)
				}
				if e = r2.Cleanup(ctx); e != nil {
					t.Fatal(e)
				}
			}
			if f.nft {
				t.Fatal("owned nft rules remain")
			}
			for _, d := range testLinks() {
				b, _ := os.ReadFile(filepath.Join(r.ProcRoot, d.Name, "rp_filter"))
				if strings.TrimSpace(string(b)) != "1" {
					t.Fatalf("rp_filter not restored for %s", d.Name)
				}
			}
			if _, e = os.Stat(r.StatePath); !os.IsNotExist(e) {
				t.Fatal("recovery state not removed")
			}
		})
	}
}
func TestForeignTableNeverDeleted(t *testing.T) {
	f := &fakeRunner{collision: true}
	r := setupRouting(t, f)
	p, _ := RoutePlan("balance", testLinks(), 18081)
	if e := r.Apply(context.Background(), p); e == nil {
		t.Fatal("ownership collision accepted")
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.Args, " "), "delete") || strings.Contains(strings.Join(c.Args, " "), "flush") {
			t.Fatal("foreign state modified")
		}
	}
}
