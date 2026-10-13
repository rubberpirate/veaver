# Linkweave

This is an independent NetworkManager extension. Do not import or modify Tosen.

Apply Ponytail (`~/.codex/skills/ponytail/SKILL.md`): use the smallest complete implementation, keep trust-boundary validation, and test nontrivial behavior.

Read README.md and docs/ARCHITECTURE.md before network changes. Never apply routing/firewall changes on a developer's host merely to test them. Use isolated network namespaces and synthetic links. Commands must use argument arrays, never interpolated shell code. Preserve foreign routes/firewall rules and rollback only owned resources. Dashboard mode/state must describe actual capabilities and distinguish connection balancing from single-stream bonding.

Verify with `go test -race ./...`, `go vet ./...`, `go build ./cmd/linkweave`, frontend checks, and namespace integration tests when privileges are available. Installed low-level skills: kernel-security and linux-perf from mohitmishra786/low-level-dev-skills. Do not change host perf/security settings for profiling.
