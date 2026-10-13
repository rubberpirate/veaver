# Veaver

> **NetworkManager extension for real-time network observability, multi-uplink load balancing, and transparent MPTCP relay bonding — all in one Go binary, no cloud required.**

---

## Why Veaver?

Most Linux networking tools either observe traffic passively or route it blindly. Veaver bridges that gap:

- **Visibility without complexity** — a btop-style terminal dashboard shows live interface rates, gateway health, journal logs, and diagnostics in one screen.
- **Multi-uplink balancing without a kernel module** — weighted round-robin across ISP links uses only nftables + policy routing; no eBPF, no kernel patch.
- **MPTCP relay bonding without application changes** — transparently intercepts host TCP, tunnels over MPTCP to a relay server, and lets applications see a plain TCP connection. No proxy config, no certificate replacement.
- **Safe by design** — every routing change is previewed before apply; rollback is automatic on failure; foreign routes and firewall rules are never touched.

---

## What It Does

| Mode | What happens | What you get |
|---|---|---|
| **Observe** | Reads NetworkManager D-Bus + kernel counters | Live rates, addresses, logs, diagnostics — zero routing changes |
| **Balance** | Kernel nft marks + policy routes | New outbound connections spread across selected uplinks by weight |
| **Relay** | Local TCP redirect → TLS/MPTCP tunnel to relay | Single-stream TCP can use multiple paths; apps need no changes |

---

## Architecture at a Glance

```
NetworkManager ──D-Bus──► monitor ──► snapshots ──► TUI / browser dashboard
       │                    │
       └──dispatcher────────┘
                            └── policy reconciliation
                                  │
                    kernel nftables + policy routes
                          │               │
                    Balance mode      Relay mode
                    direct TCP/UDP    local TCP REDIRECT
                          │               │
                    physical uplinks  TLS/MPTCP → relay
```

**Package layout:**

| Path | Role |
|---|---|
| `cmd/veaver/` | Entry point — daemon, CLI sub-commands, relay server |
| `internal/platform/` | D-Bus, kernel counters, nft/route generation, rollback |
| `internal/service/` | Config, uplink selection, sampling, reconciliation |
| `internal/transport/` | TLS CONNECT, MPTCP sockets, conntrack, duplex forwarding |
| `internal/console/` | Terminal TUI, keyboard controls, API client |
| `internal/web/assets/` | Embedded HTML/CSS/JS browser dashboard (no build step) |
| `deploy/` | systemd unit, dispatcher hook, default config |
| `tests/integration/` | Namespace-isolated kernel balancing + relay tests |

Single external dependency: `godbus/dbus/v5`. Everything else is Go stdlib.

---

## Where Things Are Stored

| Item | Path |
|---|---|
| System config | `/etc/veaver/config.json` |
| Recovery state | `/var/lib/veaver/` |
| Dashboard token | `/var/lib/veaver/dashboard-token` |
| User-mode config | `$XDG_CONFIG_HOME/veaver` → `~/.config/veaver` |
| User-mode cache | `$XDG_CACHE_HOME/veaver` → `~/.cache/veaver` |
| Built binary | `./build/veaver` (after `./scripts/build.sh`) |
| Release binaries | `./dist/veaver-linux-amd64`, `./dist/veaver-linux-arm64` |

---

## Quick Start

**Prerequisites:** Linux, NetworkManager with D-Bus, Go 1.23+

```bash
# Build
./scripts/build.sh

# Start the daemon (unprivileged → Observe mode)
./build/veaver serve

# Open the TUI in another terminal
./build/veaver tui
```

**TUI keys:** `Tab` / `1–4` switch views · `j/k` scroll · `l` load journal · `f` refresh · `q` quit
In Policy view: `o/b/r` = Observe/Balance/Relay · `Space` select uplink · `+/-` weight · `a` review · `Enter` apply · `Esc` cancel

**Optional browser dashboard:**
```bash
./build/veaver dashboard   # prints http://127.0.0.1:8787/#<token> — open in browser
```

---

## System Install (Full Routing Features)

**Additional prerequisites:** `ip` (iproute2), `nftables`, `systemd`

```bash
./scripts/build.sh
sudo ./scripts/install.sh     # installs binary + systemd unit + dispatcher hook
sudo veaver tui               # manage from TUI
```

```bash
sudo systemctl status veaver
sudo journalctl -u veaver -f
sudo veaver status
veaver doctor                 # checks permissions, MPTCP support, required tools
sudo veaver restore           # manual rollback of owned routing
sudo ./scripts/uninstall.sh
```

Cross-compile without Go on the target host:
```bash
make release   # produces ./dist/veaver-linux-amd64 and ./dist/veaver-linux-arm64
sudo env VEAVER_BINARY=./dist/veaver-linux-amd64 ./scripts/install.sh
```

---

## CLI Config Example

```json
{
  "mode": "balance",
  "interfaces": ["wlan0", "usb0"],
  "weights": {"wlan0": 3, "usb0": 1},
  "allow_metered": false
}
```

```bash
sudo veaver plan  --file ./balance.json   # preview — prints commands and warnings
sudo veaver apply --file ./balance.json   # apply
sudo veaver status
```

Weights (1–100) share **new connection counts**; they do not split a single stream or auto-detect capacity.

---

## Optional Relay Server

Run on any Linux box with a public IP and MPTCP-capable kernel:

```bash
veaver cert --hostname relay.example.com --out ./relay-creds
veaver relay --listen :7443 \
  --cert ./relay-creds/cert.pem \
  --key  ./relay-creds/key.pem \
  --token-file ./relay-creds/token
```

Copy only `cert.pem` and `token` to the client host. Configure the relay address in the TUI or browser dashboard. Keep `key.pem` on the relay.

---

## Verification

```bash
make test          # go test -race ./...
make vet           # go vet ./...
make check         # test + vet + JS/Python/shell syntax checks
make release       # cross-compile to ./dist/
./scripts/test-netns.sh   # namespace integration tests (needs ip, nft, nsenter)
```

---

## Honest Limits

- **Balance spreads new connections** — one existing TCP stream stays on one path.
- **Relay MPTCP bonding** requires a compatible kernel (Linux ≥ 5.6), relay, and both networks to support MPTCP subflows. Negotiation alone does not prove multi-path traffic.
- **IPv4 host namespace only** — containers, VMs, IPv6, UDP relay, and inbound hosting are out of scope.
- **No VPN co-existence** — activating alongside a detected VPN/WireGuard/TUN is refused.
- Tested on NetworkManager 1.58.1 + Linux 6.18; broader distribution qualification is ongoing.

---

## Docs

- [Architecture](docs/ARCHITECTURE.md)
- [Performance validation](docs/PERFORMANCE.md)
- [Verification results](docs/VERIFICATION.md)
