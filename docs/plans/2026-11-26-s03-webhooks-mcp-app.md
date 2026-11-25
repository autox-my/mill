# Slice S-03 Plan: Tailnet Webhooks (`internal/webhooks`) + Antigravity MCP App (`ui://mill/board`)

## Goal
1. Add `internal/webhooks/*` supporting tailnet-only `POST /webhooks/{name}` for Forgejo (`CT 103`) and Pulse (`CT 110`) with constant-time Bearer/HMAC auth, fail-safe 503 when unconfigured, `delivery_id` deduplication, persistent delivery log, and untrusted-payload envelope.
2. Expose `internal/webui`'s embedded board as an Antigravity MCP App resource (`ui://mill/board`) from `internal/mcp`.
3. Wire both into `internal/httpapi` and `cmd/mill/main.go`, verify via `./scripts/check.sh`, and open a PR.

```intentgate
criteria:
  - text: "internal/webhooks handles POST /webhooks/{name} for forgejo and pulse with constant-time Bearer/HMAC auth, fail-safe 503 when unconfigured, delivery_id deduplication, persistent delivery log, and untrusted-payload envelope"
    footprint: ["internal/webhooks/**"]
  - text: "internal/webui and internal/mcp expose ui://mill/board as a self-contained MCP App resource via resources/list and resources/read with live board snapshot support"
    footprint: ["internal/webui/**", "internal/mcp/**"]
  - text: "httpapi and cmd/mill wire webhooks into mill serve and verify end-to-end CLI/HTTP/MCP behavior with REQ-0013 and REQ-0014 claims passing req canon and req gate"
    footprint: ["internal/httpapi/handler.go", "internal/httpapi/handler_test.go", "cmd/mill/main.go", "cmd/mill/main_test.go", "claims/REQ-0013.yml", "claims/REQ-0014.yml", "docs/plans/2026-11-26-s03-webhooks-mcp-app.md"]
```
