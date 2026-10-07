package platform

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const nmService = "org.freedesktop.NetworkManager"
const nmPath = dbus.ObjectPath("/org/freedesktop/NetworkManager")

type Interface struct {
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	State        string  `json:"state"`
	Connectivity string  `json:"connectivity"`
	Connected    bool    `json:"connected"`
	Managed      bool    `json:"managed"`
	Metered      bool    `json:"metered"`
	Physical     bool    `json:"physical"`
	Address      string  `json:"address"`
	Prefix       uint32  `json:"prefix"`
	Gateway      string  `json:"gateway"`
	MTU          uint32  `json:"mtu"`
	RXBytes      uint64  `json:"rx_bytes"`
	TXBytes      uint64  `json:"tx_bytes"`
	RXRate       float64 `json:"rx_rate"`
	TXRate       float64 `json:"tx_rate"`
	RXErrors     uint64  `json:"rx_errors"`
	TXErrors     uint64  `json:"tx_errors"`
	RXDropped    uint64  `json:"rx_dropped"`
	TXDropped    uint64  `json:"tx_dropped"`
	Selected     bool    `json:"selected"`
}

type Network struct {
	Version      string      `json:"version"`
	State        string      `json:"state"`
	Connectivity string      `json:"connectivity"`
	VPNActive    bool        `json:"vpn_active"`
	Interfaces   []Interface `json:"interfaces"`
}

type NM struct{ conn *dbus.Conn }

func ConnectNM() (*NM, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("system D-Bus: %w", err)
	}
	return &NM{c}, nil
}
func (n *NM) Close() { _ = n.conn.Close() }

func (n *NM) props(ctx context.Context, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var p map[string]dbus.Variant
	err := n.conn.Object(nmService, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, iface).Store(&p)
	return p, err
}
func value[T any](p map[string]dbus.Variant, key string) T {
	var zero T
	if v, ok := p[key]; ok {
		if x, ok := v.Value().(T); ok {
			return x
		}
	}
	return zero
}

func (n *NM) Snapshot(ctx context.Context) (Network, error) {
	p, err := n.props(ctx, nmPath, nmService)
	if err != nil {
		return Network{}, fmt.Errorf("NetworkManager is unavailable: %w", err)
	}
	out := Network{Version: value[string](p, "Version"), State: NMState(value[uint32](p, "State")), Connectivity: connectivity(value[uint32](p, "Connectivity")), Interfaces: []Interface{}}
	for _, path := range value[[]dbus.ObjectPath](p, "ActiveConnections") {
		cp, e := n.props(ctx, path, nmService+".Connection.Active")
		if e == nil && (value[bool](cp, "Vpn") || value[string](cp, "Type") == "wireguard" || value[string](cp, "Type") == "tun") {
			out.VPNActive = true
		}
	}
	var paths []dbus.ObjectPath
	if err := n.conn.Object(nmService, nmPath).CallWithContext(ctx, nmService+".GetDevices", 0).Store(&paths); err != nil {
		return out, err
	}
	for _, path := range paths {
		dp, e := n.props(ctx, path, nmService+".Device")
		if e != nil {
			continue
		}
		name := value[string](dp, "Interface")
		if !ValidInterface(name) {
			continue
		}
		typ := value[uint32](dp, "DeviceType")
		state := value[uint32](dp, "State")
		d := Interface{Name: name, Kind: deviceKind(typ), State: DeviceState(state), Connectivity: connectivity(value[uint32](dp, "Ip4Connectivity")), Connected: state == 100, Managed: value[bool](dp, "Managed"), Physical: typ == 1 || typ == 2 || typ == 5 || typ == 8 || typ == 9, MTU: value[uint32](dp, "Mtu")}
		metered := value[uint32](dp, "Metered")
		d.Metered = metered == 1 || metered == 3
		ipath := value[dbus.ObjectPath](dp, "Ip4Config")
		if ipath != "" && ipath != "/" {
			ip, e := n.props(ctx, ipath, nmService+".IP4Config")
			if e == nil {
				d.Gateway = value[string](ip, "Gateway")
				for _, address := range value[[]map[string]dbus.Variant](ip, "AddressData") {
					addr := value[string](address, "address")
					if parsed, e := netip.ParseAddr(addr); e == nil && parsed.Is4() {
						d.Address = addr
						d.Prefix = value[uint32](address, "prefix")
						break
					}
				}
			}
		}
		d.RXBytes = counter(name, "rx_bytes")
		d.TXBytes = counter(name, "tx_bytes")
		d.RXErrors = counter(name, "rx_errors")
		d.TXErrors = counter(name, "tx_errors")
		d.RXDropped = counter(name, "rx_dropped")
		d.TXDropped = counter(name, "tx_dropped")
		out.Interfaces = append(out.Interfaces, d)
	}
	sort.Slice(out.Interfaces, func(i, j int) bool { return out.Interfaces[i].Name < out.Interfaces[j].Name })
	return out, nil
}

func counter(name, key string) uint64 {
	b, e := os.ReadFile(filepath.Join("/sys/class/net", name, "statistics", key))
	if e != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return v
}
func ValidInterface(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
func Eligible(i Interface, allowMetered bool) bool {
	// Unknown remains eligible when connectivity checking is disabled in NM.
	healthy := i.Connectivity != "none" && i.Connectivity != "portal" && i.Connectivity != "limited"
	return healthy && i.Physical && i.Managed && i.Connected && i.Address != "" && i.Gateway != "" && (!i.Metered || allowMetered)
}
func DeviceState(s uint32) string {
	switch s {
	case 10:
		return "unmanaged"
	case 20:
		return "unavailable"
	case 30:
		return "disconnected"
	case 40, 50, 60, 70, 80, 90:
		return "connecting"
	case 100:
		return "connected"
	case 110:
		return "disconnecting"
	case 120:
		return "failed"
	}
	return "unknown"
}
func NMState(s uint32) string {
	switch s {
	case 10:
		return "asleep"
	case 20:
		return "disconnected"
	case 30:
		return "disconnecting"
	case 40:
		return "connecting"
	case 50:
		return "local"
	case 60:
		return "site"
	case 70:
		return "connected"
	}
	return "unknown"
}
func connectivity(s uint32) string {
	switch s {
	case 1:
		return "none"
	case 2:
		return "portal"
	case 3:
		return "limited"
	case 4:
		return "full"
	}
	return "unknown"
}
func deviceKind(t uint32) string {
	switch t {
	case 1:
		return "Ethernet / tether"
	case 2:
		return "Wi-Fi"
	case 5:
		return "Bluetooth"
	case 8, 9:
		return "Cellular"
	case 13:
		return "Bridge"
	case 16:
		return "TUN"
	case 29:
		return "WireGuard"
	}
	return "Virtual / other"
}
