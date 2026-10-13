# Initial verification — 2026-10-10 (before the Veaver rename)

Tested development host: Linux amd64, NetworkManager 1.58.1, kernel 6.18.52-1-cachyos-lts. This is a development verification record, not a universal compatibility or performance certification.

| Check | Result |
| --- | --- |
| `go test -race ./...` | Passed: routing plans/rollback/recovery, foreign-resource protection, selected-link unplug/metering, service snapshots/config, dashboard trust boundaries, TLS relay authentication/cancellation and bidirectional transfer. Namespace-only tests skip in the ordinary suite. |
| `go vet ./...` | Passed. |
| Node JavaScript syntax check | Passed. |
| Shell script syntax checks | Passed. |
| Isolated kernel routing test | Passed real nft parsing, fwmark route selection, restoration, and both mode configurations. |
| Isolated two-uplink traffic test | Six ordinary application connections used both simulated source addresses; payloads matched. |
| Transparent relay test | A 2 MiB application upload/download traversed the authenticated relay and matched byte for byte; actual socket negotiated MPTCP, with no TCP fallback. This checks forwarding and negotiation, not a guaranteed aggregate throughput ratio. |
| Endpoint ownership/reconciliation | Tests preserve foreign endpoints and surviving owned endpoints, and remove temporary owned endpoints on cleanup. |
| Live Observe dashboard | Connected to the actual host NetworkManager without applying host routing rules. |
| Chromium desktop/mobile check | Passed authentication, live interfaces, activity/journal logs, filtering, refresh, mode review without apply, no horizontal overflow at 390 px, and no browser errors/missing assets. |
| Native/arm64 static builds | Passed; frontend assets are embedded. |
| Installer staging | Passed with `DESTDIR`, without installing a host service. |
| systemd unit validation | Passed against the staged executable. The original installed-path unit naturally requires `/usr/local/bin/veaver` to exist after installation. |

The host's routing policy was not changed or enabled during live dashboard verification. Network-changing tests ran only in fresh user/network namespaces and refused execution if host interfaces were present. A new `/proc` mount was unavailable on this host, so the harness uses user/network namespace isolation without a mount namespace.

Not checked: real Wi-Fi plus tethering throughput/power consumption, production relay capacity, sustained path-failure/reordering stress, broad distro/old-NetworkManager compatibility, or a host systemd install. The current coverage excludes IPv6 aggregation, UDP relay bonding, and separate container/VM namespaces. See PERFORMANCE.md for the qualification matrix.

Ponytail was installed and read before code was written. Its skill and the selected kernel-security/linux-perf skills are installed in the user's Codex skills directory. Test screenshots are local ignored artifacts, as they can contain network addresses or log entries.

## Version 0.2 verification — 2026-10-13

- Added a btop-style Linux terminal console with live RX/TX history, physical uplinks, diagnostics, bounded logs/journal, draft policy editing, review/apply, and compact resizing. No terminal library or additional runtime dependency was added.
- `make check build release` passed: race suite, Go vet, frontend/test JavaScript syntax, Python test-script syntax, shell syntax, native build and Linux amd64/arm64 releases.
- Namespace tests passed: exact 12:4 TCP connection distribution for 3:1 weights, byte integrity across eight concurrent 512 KiB round trips, UDP echo over both synthetic uplinks, and an established transfer surviving an atomic weight update.
- Transparent relay passed a 2 MiB upload/download with MPTCP negotiation, zero TCP fallback, and a kernel-reported peak of **two subflows**. Subflow count alone does not establish throughput aggregation or useful payload on both paths.
- Live pseudo-terminal checks passed against a temporary Observe daemon: views, live data, draft editing, Enter without review doing nothing, review, arrow navigation, cancellation, resize, quit and exact restoration of terminal settings. No policy was applied.
- Chromium desktop/mobile checks passed with invalid-weight feedback, valid weighted review, authentication, live interfaces, logs, refresh, no overflow and no browser errors. No policy was applied.
- Unit tests cover unhealthy/captive portal exclusion, unknown-connectivity compatibility, map isolation, retrying transient routing failures without a network change, atomic scheduler replacement, terminal escape sanitization, key decoding, strict loopback URLs and redirect rejection, and kernel-info version handling.

The existing installed daemon/service was not upgraded or restarted. Only a separate temporary Observe daemon was used for live checks, then stopped. Real Wi-Fi/tethering speed, asymmetric congestion, sustained path failure and broad distro qualification remain unmeasured. Connection weights distribute new flow counts, not bytes; single-stream bonding still requires the relay and working MPTCP paths.
