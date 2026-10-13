# Performance qualification

Direct mode should preserve ordinary kernel-networking performance. Relay mode adds TLS/proxy/relay processing and can improve throughput when independent uplinks are the bottleneck. These are engineering expectations, not measured production guarantees.

Do not benchmark Linkweave's manager CPU against NetworkManager's manager CPU and label that application throughput. Compare actual transfers under equivalent link conditions.

## Required comparison

Use the same machine and destination and repeat each measurement at least five times:

1. Stock NetworkManager without Linkweave.
2. Observe mode.
3. Balance mode, one uplink and both uplinks: a single stream, then multiple concurrent TCP/UDP streams.
4. Relay mode, one uplink and both: a single large upload/download, then concurrent application transfers.

Use a relay/destination whose capacity exceeds the sum of the uplinks. Check received file hashes, interface counters, negotiated MPTCP and actual subflow information. Avoid interpreting protocol overhead, retries, or background traffic as useful payload.

Record payload throughput, interface wire rates, p50/p95 latency, CPU, memory, packet loss, and connection/reconnection times. Test asymmetric links, unequal RTT, data caps, disconnect/reconnect, address changes, relay outages, and mode transitions. Observe UDP/QUIC and IPv6 behavior separately from TCP bonding.

An initial acceptance target is at least 95% of matched stock throughput in Observe/direct/bypass paths, with latency/CPU budgets chosen from the actual baseline. Relay throughput must show a useful improvement on genuinely independent constrained links; a percentage of theoretical summed capacity is not promised. Keep a poor path idle/standby when adding it reduces useful performance; manual connection weights are supported, but adaptive capacity measurement/scheduling is not implemented.

## Profiling

Capture a controlled workload with `perf stat` / `perf record` when the host already grants appropriate permissions. Do not weaken global security/perf settings to make a diagnostic command succeed. Examine sampling/allocations, D-Bus polling, journal reads, and forwarding buffer use before adding a more complex backend.

Observe/Balance forwards no packets through the Go process; expected overhead is interface/property sampling every two seconds plus dashboard clients. Journal logs are read on demand. Relay uses one TLS/MPTCP connection per application connection, avoiding a single ordered tunnel that would share head-of-line blocking across unrelated flows.

Actual Wi-Fi plus USB-tethering throughput, power consumption, and multi-distribution qualification have not been measured in the initial development environment.
