# Architecture

Veaver is a NetworkManager companion extension, not a NetworkManager source fork or a VPN service plugin. It uses the stable public D-Bus API and standard dispatcher lifecycle hooks. A formal VPN profile/editor plugin is not required by the implemented transparent-interception backend and is not included.

```text
NetworkManager ──D-Bus──► monitor ──► snapshots ──► authenticated TUI / optional browser
       │                    │            │
       └──dispatcher────────┘            └── policy reconciliation
                                              │
                     kernel nftables + policy routes + connection tracking
                              │                         │
                         Balance mode                Relay mode
                         direct TCP/UDP         local TCP REDIRECT proxy
                              │                         │
                         physical uplinks        TLS / MPTCP to relay
                                                        │
                                             ordinary TCP to destination
```

## Modules

- `cmd/veaver`: daemon, CLI, diagnostic inspection, credential generation, relay server, recovery command.
- `internal/platform`: NetworkManager D-Bus properties, Linux hardware counters, diagnostics, argument-array command execution, deterministic policy-route/nft generation, atomic recovery state, owned-resource rollback.
- `internal/service`: configuration, uplink selection, two-second sampling, bounded activity/history, serialization of policy operations, reconciliation after network changes, relay lifecycle.
- `internal/transport`: TLS CONNECT protocol, credential verification, bounded concurrent streams, standard Go MPTCP sockets, conntrack original-destination lookup, duplex forwarding and payload metrics.
- `internal/console`: terminal dashboard, safe terminal rendering, keyboard controls, and the shared authenticated loopback API client.
- `internal/web`: embedded buildless HTML/CSS/JS dashboard and authenticated HTTP API.
- `deploy`: systemd unit, observe config, short dispatcher hook.
- `tests/integration`: isolated client/peer topology exercising kernel balancing and transparent relay traffic.

`godbus/dbus/v5` is the single dependency. The frontend uses browser APIs and system fonts; Go supplies HTTP/TLS/cryptography, concurrency, CLI parsing, JSON and embedding.

## Direct connection balancing

An IPv4 nft route-output chain assigns a deterministic weighted round-robin uplink mark to a new, unmarked original-direction TCP/UDP connection. It restores that connection's mark on subsequent packets. Each uplink has a dedicated IPv4 policy table with a connected route and default gateway. Postrouting SNAT maps the original source to the selected interface address; connection tracking reverses NAT for inbound responses. SYN MSS is clamped against the selected route MTU.

Bypass checks preserve local/private/shared-address destinations. Pre-existing marks are not replaced, and only traffic initially routed to a selected physical interface is eligible. No packet is queued to userspace in Balance mode. Optional integer interface weights distribute new connection counts; the scheduler does not estimate link capacity or split one connection.

NetworkManager retains physical addressing and connection activation. Every address/gateway/interface change generates a new policy signature. Matching signatures avoid unnecessary routing operations. Selected links that disappear or fail NM IPv4 connectivity checks are removed from new-flow distribution; ordinary established connections on the missing ISP are not migrated.

## Transparent relay

The relay nft output-NAT chain redirects eligible new IPv4 TCP connections to a loopback TCP listener. Linux `SO_ORIGINAL_DST` supplies the pre-redirect address and port. A TLS-authenticated CONNECT request opens that destination from the relay. Application TLS remains opaque inside this stream.

Client relay sockets use a bypass mark to prevent interception loops and request MPTCP via the Go socket API. The initial control connection is not pinned with `SO_BINDTODEVICE`; the kernel path manager governs subflows. Existing NetworkManager/administrator-owned endpoints are preserved; missing client endpoints are created temporarily with recorded IDs and removed on cleanup. Source-specific rules route each selected source address through its gateway. Global MPTCP limits remain administrator-owned. A successful connection can still fall back to ordinary TCP; the service records that distinction. Linux 6.8+ MPTCP_INFO is sampled once per second during relay sessions to record the historical peak total subflows. Unsupported older kernels report no sample, not an invented count. This does not assert that every path carries payload. There is no custom cryptographic or congestion-control protocol.

The relay accepts only literal public destination addresses, verifies a strong bearer token inside TLS, enforces handshake deadlines and connection quotas, preserves half-close semantics, and closes streams on shutdown. LAN/metadata/private targets are blocked by the server. IPv6 is permitted as a relay destination internally but is not intercepted on the host by the current IPv4-only backend.

## Ownership and recovery

One daemon holds an exclusive state-directory lock. Policy application refuses collisions with foreign tables/rules, validates nft syntax, saves intended resource ownership before mutations, then changes only its reserved resources. An apply failure triggers cleanup; a rejected service configuration attempts to restore its previous policy.

The service temporarily uses loose reverse-path filtering on selected uplinks, records prior values, and restores them when the current value still matches its temporary setting. It never changes DNS, NetworkManager profiles, global MPTCP limits, or another nft table. Systemd `ExecStopPost` recovers after an abnormal process exit; daemon startup also recovers recorded resources. Missing kernel routing tables after reboot are treated as already cleaned up.

Weight-only or same-topology changes replace the owned nft table in one transaction, preserving routes, marks and established transfers. Topology/mode reconciliation still removes/recreates owned nft/policy resources rather than atomically swapping an entire route transaction. Failed activation retries after 30 seconds even without a topology change. The nft load itself is transactional. Route transitions can briefly interrupt traffic; assess this under load before production rollout. Never mix host networking tests with the namespace test harness.

## Dashboard trust boundary

Listen only on `127.0.0.1`. Every API request needs the locally generated 256-bit bearer token. Reject unknown Host headers and cross-origin requests, enforce strict JSON schemas/body limits, use a restrictive Content Security Policy, and render network/log values with text nodes. Static HTML contains no secrets. Diagnostic exports intentionally omit logs and credential locations, but retain local network addresses.

Terminal rendering sanitizes interface, journal, and error text to prevent escape-sequence injection. The TUI shares the API token and review/apply boundary; it never runs routing commands itself. The shared CLI/TUI client rejects non-loopback URLs and redirects and bypasses environment HTTP proxies.

The API controls network administration; a token holder is a trusted local administrator. Relay paths and credential locations are administrative configuration, not untrusted remote input. The service uses a bounded root capability under systemd rather than an isolated privileged helper in this version.

## Skills and provenance

Ponytail was installed from `DietrichGebert/ponytail` using the Codex skill installer and read before project code was written. Its instructions are applied in this project. `kernel-security` and `linux-perf` were installed from the category paths in `mohitmishra786/low-level-dev-skills`. The dashboard uses the local-only Sites workflow; no public site was created or published.

Primary references: [NetworkManager API](https://networkmanager.dev/docs/api/latest/NetworkManager.html), [dispatcher](https://networkmanager.dev/docs/api/latest/NetworkManager-dispatcher.html), [MPTCP](https://docs.kernel.org/networking/mptcp.html), [nftables](https://netfilter.org/projects/nftables/manpage.html).
