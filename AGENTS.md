# mill: agent instructions

## Project

`mill` is a headless, harness-agnostic Slice Board, Lease Engine, and Run Ledger composing `req` and `intentgate`.

## Architecture & Package Boundaries

- **Dependency Rule:** `internal/domain` must have zero external dependencies (`database/sql`, drivers, CLI frameworks). `internal/store` depends only on `internal/domain` and `database/sql` + `modernc.org/sqlite`. `cmd/mill` is a thin CLI wrapper.
- **Stage 0 (Core):** `internal/domain`, `internal/store`, `cmd/mill`.
- **Stage 1 (Adapters, parallelizable via `/swarm`):** `internal/mcp`, `internal/httpapi`, `internal/ingest`, `internal/observer`.
- **Complexity & Coverage:** Keep function cyclomatic complexity ≤ 10, maintain ≥ 70% test coverage, and write failing tests (Red → Green) before implementation code.
- **Twelve-Factor Config:** Never hardcode DB paths, tokens, or secrets; read `MILL_DB` / flags.

## Requirements (`req`)

- Check `req --version` reports `claim_schema req.claim/4` and `rules_v 9`.
- One obligation per file under `claims/REQ-NNNN.yml`. Never store derived parse/address fields in claim files.
- Run `req canon --fail-on-flag` after editing any claim.
- Run `./scripts/check.sh` before finishing any change; it must end `verdict: GREEN`.
- Never auto-merge and never sign a Baseline: ratification is the human's act.

<!-- intentgate:begin -->
## IntentGate

End every plan with exactly one fenced `intentgate` block. Each criterion is a checkable outcome, and its footprint lists the repo-relative globs it may touch. Use this shape exactly (2-space indents, `footprint` inside each criterion):

```intentgate
criteria:
  - text: "<a checkable outcome>"
    footprint: ["<repo-relative glob>"]
```

Write the plan to a file, then ask the human to run `intentgate lock --from-plan <file>` before you change code.
<!-- intentgate:end -->
