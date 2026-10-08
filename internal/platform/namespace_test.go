package platform

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These commands are allowed only in a fresh isolated network namespace.
func TestNamespaceRoutingIntegration(t *testing.T) {
	if os.Getenv("LINKWEAVE_NETNS_TEST") != "1" {
		t.Skip("run scripts/test-netns.sh for isolated kernel routing validation")
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	for _, i := range interfaces {
		if i.Name != "lo" {
			t.Fatal("refusing network test outside a fresh namespace")
		}
	}
	ctx := context.Background()
	runner := ExecRunner{}
	run := func(args ...string) {
		t.Helper()
		if _, e := runner.Run(ctx, Command{"ip", args, ""}); e != nil {
			t.Fatal(e)
		}
	}
	for _, d := range testLinks() {
		run("link", "add", d.Name, "type", "dummy")
		defer runner.Run(ctx, Command{"ip", []string{"link", "del", d.Name}, ""})
		run("addr", "add", d.Address+"/24", "dev", d.Name)
		run("link", "set", d.Name, "up")
	}
	run("link", "set", "lo", "up")
	r := NewRouting(filepath.Join(t.TempDir(), "routing.json"), runner)
	for _, mode := range []string{"balance", "relay"} {
		p, e := RoutePlan(mode, testLinks(), 18081)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.Apply(ctx, p); e != nil {
			t.Fatal(e)
		}
		for _, lease := range p.Leases {
			out, e := runner.Run(ctx, Command{"ip", []string{"-4", "route", "get", "1.1.1.1", "mark", fmt.Sprintf("0x%x", lease.Mark)}, ""})
			if e != nil || !strings.Contains(out, "dev "+lease.Interface) {
				t.Fatalf("policy route mismatch: %s %v", out, e)
			}
		}
		if e = r.Cleanup(ctx); e != nil {
			t.Fatal(e)
		}
	}
	out, e := runner.Run(ctx, Command{"ip", []string{"-4", "rule", "show"}, ""})
	if e != nil || strings.Contains(out, "0x4c57") {
		t.Fatal("routing cleanup failed", out, e)
	}
}
