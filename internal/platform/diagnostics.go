package platform

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func Checks(network Network, nmError string) []Check {
	out := []Check{{"linux", "Linux", "ok", runtime.GOOS + " / " + runtime.GOARCH}}
	if runtime.GOOS != "linux" {
		out[0].Status = "error"
	}
	status, detail := "ok", "NetworkManager "+network.Version
	if nmError != "" {
		status, detail = "error", nmError
	}
	out = append(out, Check{"networkmanager", "NetworkManager D-Bus", status, detail})
	for _, tool := range []string{"ip", "nft", "journalctl"} {
		status, detail = "ok", "Available"
		if _, e := exec.LookPath(tool); e != nil {
			status, detail = "warning", "Install "+tool+" for its associated feature"
		}
		out = append(out, Check{tool, tool, status, detail})
	}
	status, detail = "ok", "Network administration available"
	if !hasNetAdmin() {
		status, detail = "warning", "Observe mode works; routing needs CAP_NET_ADMIN"
	}
	out = append(out, Check{"privileges", "Routing permissions", status, detail})
	b, e := os.ReadFile("/proc/sys/net/mptcp/enabled")
	status, detail = "ok", "Kernel MPTCP is enabled; actual negotiation and subflows still need verification"
	if e != nil || strings.TrimSpace(string(b)) != "1" {
		status, detail = "warning", "Relay can forward over ordinary TCP; bonding needs kernel MPTCP"
	}
	out = append(out, Check{"mptcp", "Multipath TCP", status, detail})
	if network.VPNActive {
		out = append(out, Check{"vpn", "Existing VPN", "warning", "Routing modes refuse activation while a NetworkManager VPN is active"})
	}
	count := 0
	for _, d := range network.Interfaces {
		if Eligible(d, false) {
			count++
		}
	}
	status, detail = "ok", "At least two unmetered uplinks are available"
	if count < 2 {
		status, detail = "warning", "Fewer than two eligible unmetered uplinks; review interfaces and metered policy"
	}
	out = append(out, Check{"uplinks", "Multiple uplinks", status, detail})
	out = append(out, Check{"coverage", "Protocol coverage", "info", "Balance: host IPv4 TCP/UDP connections. Relay: host IPv4 TCP. IPv6, LAN, containers and existing marked flows stay on stock routing."})
	return out
}
func hasNetAdmin() bool {
	b, e := os.ReadFile("/proc/self/status")
	if e != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			v, _ := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "CapEff:")), 16, 64)
			return v&(1<<12) != 0
		}
	}
	return false
}
func JSON(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) }
