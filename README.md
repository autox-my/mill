# mill

`mill` is a headless, harness-agnostic **Slice Board, Lease Engine, and Run Ledger** for AI software factories, built to compose [`req`](https://github.com/autox-my/req) and [`intentgate`](https://github.com/autox-my/intent-gate).

Any agent harness (Antigravity, OpenCode, Claude Code, DeepSeek Harness, GitHub/Forgejo Actions) can create and claim slices, renew leases, update lifecycle states, and record run telemetry against one shared SQLite store—locally or over HTTPS on a single-binary server.

## Design Boundaries

- **Governance stays in Git (`req` + `intentgate`):** `claims/`, `.intent/`, and `scripts/check.sh` remain the deterministic source of truth. `mill` never mutates `claims/` or ratifies `.intent/` locks.
- **Single Static Binary:** Pure-Go (`modernc.org/sqlite`, zero CGO) with an embedded read-only Web UI (`//go:embed`). Runs locally with zero setup or on a 512 MB Linux LXC (< 15 MB RSS).

## Quickstart (Local SQLite)

```sh
# Build
go build -o build/mill ./cmd/mill

# Initialize local SQLite store (defaults to .mill/mill.db, or set MILL_DB)
./build/mill init

# Create, claim, renew, and advance a slice
./build/mill slice create --id S-01 --repo autox-my/mill --title "Bootstrap slice board" --claims REQ-0001,REQ-0002
./build/mill slice claim --id S-01 --owner antigravity@mac --ttl 30m
./build/mill slice renew --id S-01 --owner antigravity@mac --ttl 30m
./build/mill slice update --id S-01 --state green --verdict GREEN

# Record run telemetry and inspect summary
./build/mill run record --slice S-01 --harness antigravity --phase green --verdict GREEN --duration 42
./build/mill run list --slice S-01
./build/mill stats
```

## Multi-Harness MCP (`mill mcp`)

`mill mcp` serves stdio JSON-RPC 2.0 (`2024-11-05`) with 6 tools:
`create_slice`, `list_slices`, `claim_slice`, `renew_lease`, `update_slice`, `record_run`.

When `MILL_URL` and `MILL_API_TOKEN` are set, `mill mcp` and all CLI client commands transparently route to your remote `mill serve` instance instead of local SQLite:

```json
{
  "mcpServers": {
    "mill": {
      "command": "/Users/fd/.local/bin/mill",
      "args": ["mcp"],
      "env": {
        "MILL_URL": "https://mill.example.ts.net",
        "MILL_API_TOKEN": "<MILL_API_TOKEN>"
      }
    }
  }
}
```

## HTTP Server & Embedded Web UI (`mill serve`)

```sh
MILL_DB=/var/lib/mill/mill.db MILL_ADDR=127.0.0.1:8080 MILL_API_TOKEN="<secret>" mill serve
```

- **API Auth:** All data routes (`/slices`, `/runs`, `/stats`, `/observer/report`) require either `Authorization: Bearer <MILL_API_TOKEN>` or a validated `mill_session` cookie (`POST /auth/login`, constant-time verified, `HttpOnly; SameSite=Strict`).
- **Read-only Web UI (`GET /`):** Board (5-lane Kanban with live lease countdowns), filterable Run Ledger, Slice Detail drawer, and Observer Hotspots with 1-click **Copy report JSON**.

## Advisory CI Ingest & Observer (`mill ingest` / `mill improve`)

```sh
# Parse check.sh, intentgate verify --format json, or req diff into the Run Ledger
# Automatically extracts S-NN from branch names like feature/s-01-docs-refresh -> S-01
./scripts/check.sh | mill ingest --source check --branch feature/s-01-docs-refresh --harness github-actions --phase ci

# Generate a read-only hotspot analysis JSON for any agent harness (no external LLM key required)
mill improve --report-only
```

See [`deploy/proxmox/README.md`](deploy/proxmox/README.md) for single-binary Debian LXC + Tailscale Serve deployment and [`.github/workflows/ci.yml`](.github/workflows/ci.yml) for ephemeral Tailscale (`tag:ci`) GitHub Actions ingest.

## Verification

```sh
./scripts/check.sh
```
