# mill

`mill` is a headless, harness-agnostic **Slice Board, Lease Engine, and Run Ledger** for AI software factories, built to compose [`req`](https://github.com/autox-my/req) and [`intentgate`](https://github.com/autox-my/intent-gate).

Any agent harness (Claude Code, Antigravity, OpenCode, DeepSeek Harness, CI runners) can claim slices, renew leases, update lifecycle states, and record run telemetry against one shared SQLite store—without forcing developers into a separate chat UI.

## Design Boundaries

- **Governance stays in Git (`req` + `intentgate`):** `claims/`, `.intent/`, and `scripts/check.sh` remain the deterministic source of truth. `mill` never mutates `claims/` or ratifies `.intent/` locks.
- **Stage 0 (Walking Skeleton):** Pure-Go core domain (`internal/domain`), SQLite store with atomic lease semantics (`internal/store`), and thin CLI (`cmd/mill`).
- **Stage 1 (Parallel Adapters):** MCP server (`mill mcp`), HTTP/Tailscale server (`mill serve`), telemetry ingest (`internal/ingest`), and self-improvement outer-loop Draft PR observer (`internal/observer`).

## Quickstart

```sh
# Build
go build -o build/mill ./cmd/mill

# Initialize local SQLite store (defaults to .mill/mill.db, or set MILL_DB)
./build/mill init

# Create, claim, renew, and advance a slice
./build/mill slice create --id S-01 --repo autox-my/mill --title "Bootstrap slice board" --claims REQ-0001,REQ-0002
./build/mill slice claim --id S-01 --owner claude-code@mac --ttl 30m
./build/mill slice renew --id S-01 --owner claude-code@mac --ttl 30m
./build/mill slice update --id S-01 --state red
./build/mill slice update --id S-01 --state green --verdict GREEN

# Record run telemetry and view summary
./build/mill run record --slice S-01 --harness claude-code --phase green --verdict GREEN --duration 42
./build/mill run list --slice S-01
./build/mill stats
```

## Verification

```sh
./scripts/check.sh
```
