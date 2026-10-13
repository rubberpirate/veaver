# Linkweave

A standalone NetworkManager extension with a terminal dashboard for network diagnostics, traffic statistics, logs, and multi-uplink routing. It is an independent project and does not depend on Tosen.

Linkweave uses NetworkManager's public D-Bus API and dispatcher hooks. It does not replace or fork NetworkManager. The btop-style TUI and optional browser dashboard ship in one Go executable. No frontend runtime or cloud service is required. Run `linkweave` or `linkweave tui` to open the terminal console against a running daemon.

## What works

- **Observe:** live interface receive/transmit rates, counters, addresses, gateways, connection/metered status, capability checks, service activity, and NetworkManager journal messages. No routing changes.
- **Balance connections:** weighted kernel-based distribution of new outbound host IPv4 TCP/UDP connections across selected interfaces. Connection marks preserve the chosen uplink; source NAT uses its address. Both downloads and uploads benefit across independent connections.
- **Relay bonding:** transparent host IPv4 TCP interception and authenticated TLS forwarding over an MPTCP-capable connection to a Linux relay. Applications and destination servers need no proxy configuration or HTTPS certificate replacement. MPTCP negotiation, ordinary-TCP fallback, and the peak kernel-reported subflow count (Linux 6.8+) are reported.
- Uplink selection, metered opt-in, configuration preview, stock-routing restore, unplug/address-change reconciliation, crash recovery, a CLI, and systemd packaging.

One ordinary connection cannot aggregate independent ISP links in Balance mode. Relay mode can support single-stream TCP aggregation when the kernel, endpoint/subflow configuration, both networks, and relay support it. MPTCP negotiation alone does not prove multiple active paths or faster transfers.

## Quick start

Requires Linux and NetworkManager with working system D-Bus. Build with Go 1.23+:

```bash
./scripts/build.sh
./build/linkweave serve
```

In another terminal, open the TUI:

```bash
./build/linkweave tui
```

Use `Tab` or `1`–`4` for Overview, Diagnostics, Logs, and Policy; `j/k` or arrows to move; `l` to load the journal; `f` to refresh; `q` to quit. In Policy, choose `o/b/r` for Observe/Balance/Relay, `Space` to select an uplink, `+/-` for its weight, `m` for metered opt-in, and `u` for automatic selection. Press `a` to review, then `Enter` to apply or `Esc` to cancel. `x` discards the draft. Edits do not change routing until reviewed and applied.

`./build/linkweave tui --once` prints a plain snapshot for noninteractive terminals. Relay credentials or other configuration can be loaded as a draft with `tui --file ./config.json`; the usual review still applies. A full layout needs 72 columns and 20 rows; smaller terminals show a compact view.

For the optional browser dashboard, `./build/linkweave dashboard` prints the authenticated URL. Open it in a local browser. It uses `127.0.0.1:8787`; the access token is in the URL fragment and is removed from the address bar after loading. The browser stores it only for the tab session. Alternatively, paste the access token into the login form. Treat this token as a network-administration credential.

The unprivileged daemon supports Observe mode. It uses your user config/cache directories and reports unavailable privileged features instead of attempting them.

## Install the system extension

Prerequisites for routing: NetworkManager, `ip` (iproute2), nftables, and systemd. Install missing packages using your distribution's package manager. No kernel module, eBPF program, or NetworkManager source build is needed.

```bash
./scripts/build.sh
sudo ./scripts/install.sh
sudo linkweave tui
```

The installer preserves existing configuration and starts **Observe** mode on a fresh installation. It installs only the executable, a hardened systemd unit, and a short NetworkManager dispatcher notification hook. Enable a routing mode through the TUI or optional browser dashboard after reviewing its commands and warnings. If a user-mode daemon already occupies port 8787, stop it before starting the system service.

Prebuilt Linux amd64/arm64 executables are produced by `make release`. To install an appropriate prebuilt executable without Go on the destination PC, supply `LINKWEAVE_BINARY` to the installer, for example `sudo env LINKWEAVE_BINARY=./dist/linkweave-linux-amd64 ./scripts/install.sh`. Packaged boot integration currently requires systemd; other init systems can run the standalone daemon with their own service definitions.

```bash
sudo systemctl status linkweave
sudo journalctl -u linkweave -f
sudo linkweave restore
sudo ./scripts/uninstall.sh
```

Uninstall restores owned routing before removing the service and retains configuration/state for inspection. To package without changing a host:

```bash
DESTDIR=/tmp/linkweave-package ./scripts/install.sh
```

## Terminal and optional browser dashboards

- Overview: aggregate physical-interface upload/download, active uplinks, NetworkManager state, and traffic history.
- Interfaces: live rates, IPv4/gateway, connection state, metering, error/drop counters.
- Diagnostics: permissions, kernel MPTCP, required tools, VPN conflicts, and coverage limitations.
- Policy: Observe / Balance / Relay, selected interfaces, metered opt-in, and a review dialog before applying.
- Logs: bounded daemon events plus on-demand system journal access and message filtering.
- Diagnostic export: includes interface/capability data but excludes access tokens, relay credential paths, and logs. It still contains local network addresses; inspect it before sharing.

Counters include all physical-interface traffic and protocol overhead, not just application payload. Totals are cumulative NIC counters since creation/reset. Samples refresh every two seconds and keep three minutes of rate history. The daemon leaves the packet path to the kernel in Observe/Balance modes.

## Optional Linux relay

A relay needs sufficient bandwidth, a reachable TCP port, and kernel MPTCP support for aggregation. It can run as an ordinary user. Generate credentials on the relay host:

```bash
linkweave cert --hostname relay.example.com --out ./relay-credentials
linkweave relay --listen :7443 \
  --cert ./relay-credentials/cert.pem \
  --key ./relay-credentials/key.pem \
  --token-file ./relay-credentials/token
```

Copy **only** `cert.pem` and `token` securely to the client, for example into `/etc/linkweave/relay-ca.pem` and `/etc/linkweave/relay-token`. Keep `key.pem` on the relay. Set the relay address, TLS server name, certificate path, and token path in the dashboard. Generated certificates expire after one year; a normal publicly trusted certificate is also supported by leaving the CA path empty.

NetworkManager versions that manage MPTCP endpoints can configure `connection.mptcp-flags` on physical profiles. Linkweave preserves existing endpoints, adds temporary client subflow endpoints for selected addresses that lack them, and supplies source-specific routes. Its temporary endpoints are removed during cleanup. It does not overwrite global MPTCP limits. Confirm endpoint flags and limits on both machines with:

```bash
ip mptcp endpoint show
ip mptcp limits show
ss -Mni
```

The default kernel limits may allow fewer subflows than your selected interfaces. Use the [official MPTCP setup guidance](https://www.mptcp.dev/setup.html) and verify payload travels over the intended paths before claiming aggregation. The TUI reports negotiated connections and peak observed total subflows from Linux MPTCP_INFO on kernels 6.8+. A zero peak means no supported sample was observed; the historical peak is not a current path count or proof of traffic on every path. Use interface counters and `ss -Mni` to check actual path usage. Relay activation performs an authenticated reachability check through the relay to `1.1.1.1:443` before enabling interception.

## CLI configuration

Example Balance config:

```json
{
  "mode": "balance",
  "interfaces": ["wlan0", "usb0"],
  "allow_metered": false,
  "weights": {"wlan0": 3, "usb0": 1},
  "relay": {"address": "", "server_name": "", "ca_file": "", "token_file": ""}
}
```

```bash
sudo linkweave plan --file ./balance.json
sudo linkweave apply --file ./balance.json
sudo linkweave status
linkweave doctor
```

Weights from 1 to 100 assign shares of **new connections** in Balance mode; missing weights default to 1. A 3:1 ratio fits, for example, a roughly 300 Mbps Wi-Fi link and 100 Mbps tether. It does not promise a 3:1 byte ratio or measure capacity automatically, and it does not control the kernel MPTCP scheduler. Weight-only updates preserve routes and active connections using an atomic nft replacement.

Empty interface selection means automatic eligible interfaces. Disconnected selected interfaces are skipped while a usable one remains; metered links are excluded unless opted in. Links reported by NetworkManager as offline, captive-portal, or limited are excluded; unknown connectivity remains eligible when NM checking is disabled. Failed policies retry every 30 seconds and immediately when eligible topology changes. Linkweave never turns a connection off in NetworkManager.

The service config is `/etc/linkweave/config.json`, recovery state is `/var/lib/linkweave`, and the dashboard token is `/var/lib/linkweave/dashboard-token`. The default user-mode equivalents are `$XDG_CONFIG_HOME/linkweave` and `$XDG_CACHE_HOME/linkweave`, falling back to the usual home directories. CLI commands accept `--url` and `--state-dir` for custom instances.

## Compatibility and limits

NetworkManager running by itself does not guarantee every routing feature. Observe needs only D-Bus and ordinary interface-counter access. Balance additionally needs nftables, policy routing, connection tracking, IPv4 gateways, and network-administration permission. Relay adds a compatible server and usable MPTCP paths for aggregation. Unsupported capabilities are displayed in diagnostics.

- The implemented routing scope is traffic originating in the host namespace. Containers and VMs with separate namespaces, inbound hosting, raw sockets, multicast, and broadcast are not aggregated.
- IPv6 retains ordinary routing. Relay mode leaves UDP/QUIC direct; a relay-bound TCP session can survive path loss only if usable MPTCP subflows remain. Balance cannot migrate an established ordinary connection to a new ISP address.
- IPv4 loopback, local destinations, RFC1918/link-local/shared-address space, and pre-marked application/firewall flows are bypassed. Selected uplinks must be managed and connected with an IPv4 address/gateway.
- Activating alongside a detected NetworkManager VPN/WireGuard/TUN connection is refused. Independently managed VPNs/firewalls need separate compatibility testing; Linkweave is not a privacy VPN or a leak-prevention product.
- Routing tables 42001–43000, rule priorities 22001–23000 and 24001–25000, marks in the `0x4c57xxxx` range, and nft table `ip linkweave` are reserved. Temporary MPTCP endpoints use free IDs in 128–255. Collisions are refused; unrelated rules and NetworkManager profiles are preserved.
- Mode changes may require applications to reconnect. A retained relay listener preserves established sessions during successful uplink reconciliation; sessions affected by complete uplink loss or a suspended policy can fail.
- The initial system daemon runs with a bounded `CAP_NET_ADMIN` capability and a private, authenticated loopback API. A dedicated privileged-helper process is not implemented. Keep the token private and review changes before applying.
- Not every PC/distribution combination is tested. Native development was verified on NetworkManager 1.58.1 and Linux 6.18; Linux amd64 and arm64 binaries build. Broad distribution qualification remains release work.

## Verification

```bash
go test -race ./...
go vet ./...
node --check internal/web/assets/app.js
./scripts/test-netns.sh
make release
```

Namespace tests require unprivileged user/network namespaces, `ip`, `nft`, `nsenter`, and the relevant kernel features. They refuse to operate if the test namespace contains host interfaces. They validate nft syntax, marked routing/rollback, actual flows through two simulated uplinks, and a transparent authenticated MPTCP relay upload/download.

Optional live TUI verification uses the Python standard library: set `LINKWEAVE_TEST_STATE_DIR` and optionally `LINKWEAVE_TEST_URL`, then run `python3 scripts/test-tui.py` against a dedicated Observe daemon. It checks views, draft/review without apply, navigation, resize and terminal restoration.

Optional browser verification uses Playwright and an Observe daemon. Supply `LINKWEAVE_TEST_TOKEN_FILE`; use `LINKWEAVE_PLAYWRIGHT_PATH` and `LINKWEAVE_CHROMIUM_PATH` when reusing existing installations, then run `node scripts/test-dashboard.cjs`. The test reviews routing but does not apply it. Screenshots are ignored by Git because they can contain local addresses/logs.

See [architecture](docs/ARCHITECTURE.md), [performance validation](docs/PERFORMANCE.md), and [verification results](docs/VERIFICATION.md).
