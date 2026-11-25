# Slice S-04 Plan: Factory Telemetry, Cycle Time & Post-Slice Audit Skill

## Goal
Add spending/token/model telemetry to `RunEvent`, idempotent SQLite migration for `run_events`, cycle-time and model/harness breakdowns in `StatsSummary` and `observer.Report`, CLI/HTTP/MCP/Web UI wiring, a read-only `slice-audit` repo skill, and `claims/REQ-0015.yml`.

```intentgate
criteria:
  - text: "domain.RunEvent validates optional TokensIn, TokensOut, CostUSD and domain provides ComputeCycleTime and BuildTelemetryBreakdown"
    footprint: ["internal/domain/slice.go", "internal/domain/slice_test.go"]
  - text: "SQLite store idempotently migrates run_events telemetry columns and populates cycle time and model/harness breakdowns in Stats"
    footprint: ["internal/store/sqlite.go", "internal/store/sqlite_test.go"]
  - text: "observer.Report, HTTP API/client, MCP record_run, and CLI mill run record / mill stats expose telemetry and cycle time"
    footprint: ["internal/observer/**", "internal/httpapi/**", "internal/httpclient/**", "internal/mcp/**", "cmd/mill/**"]
  - text: "Web UI surfaces Avg cycle time, Total spend KPIs, and Model & Harness Telemetry table"
    footprint: ["internal/webui/**"]
  - text: "slice-audit skill and REQ-0015 claim exist and pass req canon and req gate"
    footprint: [".agents/skills/slice-audit/SKILL.md", "claims/REQ-0015.yml", "docs/plans/2026-11-26-s04-factory-telemetry.md"]
```
