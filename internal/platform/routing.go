package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const Table = "veaver"
const BypassMark = 0x4c570fff
const reservedMask = 0xffff0000

type Lease struct {
	Interface  string `json:"interface"`
	Table      int    `json:"table"`
	Priority   int    `json:"priority"`
	Mark       uint32 `json:"mark"`
	RPFilter   string `json:"rp_filter"`
	Source     string `json:"source,omitempty"`
	EndpointID int    `json:"endpoint_id,omitempty"`
}
type RouteState struct {
	Leases  []Lease `json:"leases"`
	OwnsNFT bool    `json:"owns_nft"`
}
type Plan struct {
	Mode       string    `json:"mode"`
	Interfaces []string  `json:"interfaces"`
	Commands   []Command `json:"commands"`
	NFT        string    `json:"nft"`
	Leases     []Lease   `json:"leases"`
	Warnings   []string  `json:"warnings"`
}

func RoutePlan(mode string, links []Interface, relayPort int) (Plan, error) {
	return WeightedRoutePlan(mode, links, relayPort, nil)
}

// Weights distribute new connections, never packets of an established connection.
func WeightedRoutePlan(mode string, links []Interface, relayPort int, weights map[string]int) (Plan, error) {
	if mode != "balance" && mode != "relay" {
		return Plan{}, errors.New("routing mode must be balance or relay")
	}
	if len(links) == 0 || len(links) > 16 {
		return Plan{}, errors.New("select between 1 and 16 connected IPv4 uplinks")
	}
	if mode == "relay" && (relayPort < 1024 || relayPort > 65535) {
		return Plan{}, errors.New("invalid relay proxy port")
	}
	p := Plan{Mode: mode, Interfaces: []string{}, Commands: []Command{}, Warnings: []string{"IPv6, LAN and containers retain ordinary routing", "Existing connections may require reconnecting when changing mode"}}
	ids := map[int]bool{}
	names := map[string]bool{}
	var choices, ifnames []string
	totalWeight := 0
	links = append([]Interface(nil), links...)
	sort.Slice(links, func(i, j int) bool { return links[i].Name < links[j].Name })
	for _, d := range links {
		if !ValidInterface(d.Name) || names[d.Name] {
			return p, errors.New("invalid or duplicate interface")
		}
		names[d.Name] = true
		addr, e := netip.ParseAddr(d.Address)
		if e != nil || !addr.Is4() || d.Prefix > 32 {
			return p, errors.New("invalid IPv4 interface address")
		}
		gw, e := netip.ParseAddr(d.Gateway)
		if e != nil || !gw.Is4() || !gw.IsGlobalUnicast() {
			return p, errors.New("invalid IPv4 gateway")
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(d.Name))
		id := int(h.Sum32()%1000) + 1
		if ids[id] {
			return p, errors.New("interface routing ID collision; choose different interfaces")
		}
		ids[id] = true
		lease := Lease{Interface: d.Name, Table: 42000 + id, Priority: 22000 + id, Mark: 0x4c570000 | uint32(id)}
		if mode == "relay" {
			lease.Source = d.Address
		}
		p.Leases = append(p.Leases, lease)
		p.Interfaces = append(p.Interfaces, d.Name)
		prefix := netip.PrefixFrom(addr, int(d.Prefix)).Masked().String()
		p.Commands = append(p.Commands, Command{"ip", []string{"-4", "route", "replace", prefix, "dev", d.Name, "src", d.Address, "table", strconv.Itoa(lease.Table)}, ""}, Command{"ip", []string{"-4", "route", "replace", "default", "via", d.Gateway, "dev", d.Name, "src", d.Address, "table", strconv.Itoa(lease.Table)}, ""}, Command{"ip", []string{"-4", "rule", "add", "priority", strconv.Itoa(lease.Priority), "fwmark", fmt.Sprintf("0x%x/0xffffffff", lease.Mark), "lookup", strconv.Itoa(lease.Table)}, ""})
		if lease.Source != "" {
			p.Commands = append(p.Commands, Command{"ip", []string{"-4", "rule", "add", "priority", strconv.Itoa(lease.Priority + 2000), "from", lease.Source, "lookup", strconv.Itoa(lease.Table)}, ""})
		}
		weight := 1
		if configured, ok := weights[d.Name]; ok {
			weight = configured
		}
		if weight < 1 || weight > 100 {
			return p, errors.New("link weights must be between 1 and 100")
		}
		for slot := 0; slot < weight; slot++ {
			choices = append(choices, fmt.Sprintf("%d : 0x%x", totalWeight, lease.Mark))
			totalWeight++
		}
		ifnames = append(ifnames, strconv.Quote(d.Name))
	}
	var b strings.Builder
	b.WriteString("table ip veaver {\n")
	b.WriteString(" set bypass { type ipv4_addr; flags interval; elements = { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4 } }\n")
	if mode == "balance" {
		b.WriteString(" chain steer { type route hook output priority mangle; policy accept;\n")
		b.WriteString(" ip daddr @bypass return\n fib daddr type local return\n")
		fmt.Fprintf(&b, " ct mark & 0xffff0000 == 0x4c570000 ct mark != 0x%x meta mark set ct mark return\n", BypassMark)
		fmt.Fprintf(&b, " oifname { %s } meta mark 0 ct mark 0 ct direction original ct state new meta l4proto { tcp, udp } ct mark set numgen inc mod %d map { %s }\n", strings.Join(ifnames, ", "), totalWeight, strings.Join(choices, ", "))
		fmt.Fprintf(&b, " ct mark & 0xffff0000 == 0x4c570000 ct mark != 0x%x meta mark set ct mark\n }\n", BypassMark)
		b.WriteString(" chain source { type nat hook postrouting priority srcnat; policy accept;\n")
		for idx, l := range p.Leases {
			fmt.Fprintf(&b, " ct mark 0x%x oifname %q counter snat to %s\n", l.Mark, l.Interface, links[idx].Address)
		}
		b.WriteString(" }\n chain mss { type filter hook postrouting priority mangle; policy accept;\n ct mark & 0xffff0000 == 0x4c570000 tcp flags syn tcp option maxseg size set rt mtu\n }\n")
	} else {
		// Only first packets reach this nat chain; established ordinary flows are untouched.
		b.WriteString(" chain proxy { type nat hook output priority dstnat; policy accept;\n ip daddr @bypass return\n fib daddr type local return\n")
		fmt.Fprintf(&b, " meta mark != 0 return\n ct mark != 0 return\n oifname { %s } meta l4proto tcp counter redirect to :%d\n }\n", strings.Join(ifnames, ", "), relayPort)
		p.Warnings = append(p.Warnings, "Relay mode forwards IPv4 TCP only; UDP/QUIC remain direct", "MPTCP fallback can forward traffic without aggregating bandwidth")
	}
	b.WriteString("}\n")
	p.NFT = b.String()
	return p, nil
}

type Routing struct {
	Runner    Runner
	StatePath string
	ProcRoot  string
	state     RouteState
	Warnings  []string
	applied   *Plan
}

func NewRouting(path string, r Runner) *Routing {
	return &Routing{Runner: r, StatePath: path, ProcRoot: "/proc/sys/net/ipv4/conf"}
}
func (r *Routing) save() error { return AtomicJSON(r.StatePath, r.state, 0600) }
func AtomicJSON(path string, v any, mode os.FileMode) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".veaver-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(append(b, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(name, path)
}
func (r *Routing) Load() error {
	b, e := os.ReadFile(r.StatePath)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &r.state); e != nil {
		return e
	}
	for _, l := range r.state.Leases {
		if !ValidInterface(l.Interface) || l.Table < 42001 || l.Table > 43000 || l.Priority != l.Table-20000 || l.Mark != 0x4c570000|uint32(l.Table-42000) || (l.RPFilter != "0" && l.RPFilter != "1" && l.RPFilter != "2") {
			return errors.New("invalid routing recovery state")
		}
		if l.Source != "" {
			addr, e := netip.ParseAddr(l.Source)
			if e != nil || !addr.Is4() {
				return errors.New("invalid relay source in recovery state")
			}
		}
		if l.EndpointID != 0 && (l.EndpointID < 128 || l.EndpointID > 255 || l.Source == "") {
			return errors.New("invalid MPTCP endpoint recovery state")
		}
	}
	return nil
}

func (r *Routing) Apply(ctx context.Context, p Plan) error {
	// Change the scheduler atomically when the route topology stays the same.
	// Keeping routes and conntrack marks preserves active uploads/downloads.
	if r.state.OwnsNFT && r.applied != nil && r.applied.Mode == p.Mode && reflect.DeepEqual(r.applied.Commands, p.Commands) {
		transaction := "delete table ip veaver\n" + p.NFT
		if _, e := r.Runner.Run(ctx, Command{"nft", []string{"-c", "-f", "-"}, transaction}); e != nil {
			return fmt.Errorf("nft preflight: %w", e)
		}
		if _, e := r.Runner.Run(ctx, Command{"nft", []string{"-f", "-"}, transaction}); e != nil {
			return e
		}
		r.applied = &p
		return nil
	}
	r.Warnings = nil
	var retained []Lease
	if p.Mode == "relay" {
		for _, old := range r.state.Leases {
			for _, next := range p.Leases {
				if old.EndpointID != 0 && old.Interface == next.Interface && old.Source == next.Source {
					retained = append(retained, old)
				}
			}
		}
	}
	if e := r.cleanup(ctx, retained); e != nil {
		return e
	}
	// Refuse ownership collisions instead of deleting another program's resources.
	if _, e := r.Runner.Run(ctx, Command{"nft", []string{"list", "table", "ip", Table}, ""}); e == nil {
		return errors.New("nft table veaver exists without our recovery state")
	}
	rules, e := r.Runner.Run(ctx, Command{"ip", []string{"-j", "-4", "rule", "show"}, ""})
	if e != nil {
		return e
	}
	var current []struct {
		Priority int `json:"priority"`
		Table    any `json:"table"`
	}
	if e = json.Unmarshal([]byte(rules), &current); e != nil {
		return e
	}
	for _, l := range p.Leases {
		for _, rule := range current {
			if rule.Priority == l.Priority || (l.Source != "" && rule.Priority == l.Priority+2000) || fmt.Sprint(rule.Table) == strconv.Itoa(l.Table) {
				return fmt.Errorf("reserved rule/table %d already in use", l.Table)
			}
		}
		routes, e := r.Runner.Run(ctx, Command{"ip", []string{"-j", "-4", "route", "show", "table", strconv.Itoa(l.Table)}, ""})
		if e == nil && strings.TrimSpace(routes) != "[]" && strings.TrimSpace(routes) != "" {
			return fmt.Errorf("routing table %d already contains routes", l.Table)
		}
	}
	if _, e = r.Runner.Run(ctx, Command{"nft", []string{"-c", "-f", "-"}, p.NFT}); e != nil {
		return fmt.Errorf("nft preflight: %w", e)
	}
	var nextLeases []Lease
	for _, l := range p.Leases {
		b, e := os.ReadFile(filepath.Join(r.ProcRoot, l.Interface, "rp_filter"))
		if e != nil {
			return e
		}
		l.RPFilter = strings.TrimSpace(string(b))
		for _, old := range retained {
			if old.Interface == l.Interface && old.Source == l.Source {
				l.EndpointID = old.EndpointID
			}
		}
		nextLeases = append(nextLeases, l)
	}
	r.state.Leases = nextLeases
	if e = r.save(); e != nil {
		r.state = RouteState{Leases: retained}
		return e
	}
	rollback := func(cause error) error { clean := r.Cleanup(context.Background()); return errors.Join(cause, clean) }
	for _, l := range r.state.Leases {
		if e = os.WriteFile(filepath.Join(r.ProcRoot, l.Interface, "rp_filter"), []byte("2"), 0644); e != nil {
			return rollback(e)
		}
	}
	for _, c := range p.Commands {
		if _, e = r.Runner.Run(ctx, c); e != nil {
			return rollback(e)
		}
	}
	if p.Mode == "relay" {
		r.addEndpoints(ctx)
	}
	r.state.OwnsNFT = true
	if e = r.save(); e != nil {
		return rollback(e)
	}
	if _, e = r.Runner.Run(ctx, Command{"nft", []string{"-f", "-"}, p.NFT}); e != nil {
		return rollback(e)
	}
	r.applied = &p
	return nil
}

func (r *Routing) Cleanup(ctx context.Context) error {
	return r.cleanup(ctx, nil)
}
func (r *Routing) cleanup(ctx context.Context, retained []Lease) error {
	r.applied = nil
	var errs []error
	if r.state.OwnsNFT {
		if _, e := r.Runner.Run(ctx, Command{"nft", []string{"list", "table", "ip", Table}, ""}); e == nil {
			if _, e = r.Runner.Run(ctx, Command{"nft", []string{"delete", "table", "ip", Table}, ""}); e != nil {
				errs = append(errs, e)
			}
		}
	}
	for _, l := range r.state.Leases {
		rules, e := r.Runner.Run(ctx, Command{"ip", []string{"-j", "-4", "rule", "show"}, ""})
		if e != nil {
			errs = append(errs, e)
			continue
		}
		var entries []struct {
			Priority int `json:"priority"`
		}
		if e = json.Unmarshal([]byte(rules), &entries); e != nil {
			errs = append(errs, e)
			continue
		}
		for _, entry := range entries {
			if entry.Priority == l.Priority {
				_, e = r.Runner.Run(ctx, Command{"ip", []string{"-4", "rule", "del", "priority", strconv.Itoa(l.Priority), "fwmark", fmt.Sprintf("0x%x/0xffffffff", l.Mark), "lookup", strconv.Itoa(l.Table)}, ""})
				if e != nil {
					errs = append(errs, e)
				}
			}
			if l.Source != "" && entry.Priority == l.Priority+2000 {
				_, e = r.Runner.Run(ctx, Command{"ip", []string{"-4", "rule", "del", "priority", strconv.Itoa(l.Priority + 2000), "from", l.Source, "lookup", strconv.Itoa(l.Table)}, ""})
				if e != nil {
					errs = append(errs, e)
				}
			}
		}
		keep := false
		for _, old := range retained {
			if old.EndpointID == l.EndpointID && old.Source == l.Source {
				keep = true
			}
		}
		if l.EndpointID != 0 && !keep {
			endpoints, e := r.endpoints(ctx)
			if e != nil {
				errs = append(errs, e)
			} else {
				for _, endpoint := range endpoints {
					if endpoint.ID == l.EndpointID && endpoint.Address == l.Source {
						if _, e = r.Runner.Run(ctx, Command{"ip", []string{"mptcp", "endpoint", "delete", "id", strconv.Itoa(l.EndpointID)}, ""}); e != nil {
							errs = append(errs, e)
						}
					}
				}
			}
		}
		if _, e = r.Runner.Run(ctx, Command{"ip", []string{"-4", "route", "flush", "table", strconv.Itoa(l.Table)}, ""}); e != nil && !strings.Contains(e.Error(), "FIB table does not exist") {
			errs = append(errs, e)
		}
		path := filepath.Join(r.ProcRoot, l.Interface, "rp_filter")
		b, e := os.ReadFile(path)
		if e == nil && strings.TrimSpace(string(b)) == "2" {
			if e = os.WriteFile(path, []byte(l.RPFilter), 0644); e != nil {
				errs = append(errs, e)
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	r.state = RouteState{Leases: retained}
	if len(retained) > 0 {
		return r.save()
	}
	if e := os.Remove(r.StatePath); e != nil && !os.IsNotExist(e) {
		return e
	}
	return nil
}

type endpoint struct {
	ID      int    `json:"id"`
	Address string `json:"address"`
}

func (r *Routing) endpoints(ctx context.Context) ([]endpoint, error) {
	out, e := r.Runner.Run(ctx, Command{"ip", []string{"-j", "mptcp", "endpoint", "show"}, ""})
	if e != nil {
		return nil, e
	}
	var result []endpoint
	e = json.Unmarshal([]byte(out), &result)
	return result, e
}
func (r *Routing) addEndpoints(ctx context.Context) {
	current, e := r.endpoints(ctx)
	if e != nil {
		r.Warnings = append(r.Warnings, "Could not configure MPTCP endpoints; forwarding may use ordinary TCP")
		return
	}
	used := map[int]bool{}
	addresses := map[string]bool{}
	for _, ep := range current {
		used[ep.ID] = true
		addresses[ep.Address] = true
	}
	for i := range r.state.Leases {
		l := &r.state.Leases[i]
		if addresses[l.Source] {
			continue
		}
		id := 128
		for id <= 255 && used[id] {
			id++
		}
		if id > 255 {
			r.Warnings = append(r.Warnings, "No free MPTCP endpoint IDs; inspect path-manager configuration")
			continue
		}
		l.EndpointID = id
		if e = r.save(); e != nil {
			l.EndpointID = 0
			r.Warnings = append(r.Warnings, "Could not record endpoint ownership; endpoint not created")
			continue
		}
		if _, e = r.Runner.Run(ctx, Command{"ip", []string{"mptcp", "endpoint", "add", l.Source, "dev", l.Interface, "subflow", "id", strconv.Itoa(id)}, ""}); e != nil {
			l.EndpointID = 0
			_ = r.save()
			r.Warnings = append(r.Warnings, "Could not add endpoint for "+l.Interface+"; inspect MPTCP support")
		} else {
			used[id] = true
			addresses[l.Source] = true
		}
	}
}
