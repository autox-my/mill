---
name: slice-audit
description: Run a read-only Argon post-slice audit over git diff main...HEAD and agent transcript.jsonl to catch tautological tests, wasted tool-call loops, and architecture drift, feeding findings into repo skill Gotchas.
---

# Post-Slice Transcript & Diff Audit (`slice-audit`)

Run this **read-only** audit (recommended model: **Gemini 4 Argon**) after a slice reaches `green` or merges, to catch quality flaws that static gates (`gofmt`, `go vet`, `req gate`) miss.

> **Governance Guardrail:** Strictly **read-only**. Never modify `claims/`, `.intent/`, or slice code directly from an audit run; surface actionable findings and append reusable prevention rules only to `## Gotchas` in `.agents/skills/*/SKILL.md` when requested.

## 1. Inputs to Inspect

1. **Diff:** `git diff main...HEAD` (or the merged slice commit range) and `git log --stat main..HEAD`.
2. **Transcript:** `<appDataDir>/brain/<conversation-id>/.system_generated/logs/transcript.jsonl` (search for repeated failing `run_command` calls, unbatched single-tool turns, and re-reads of unchanged files).
3. **Ledger context:** `mill improve --report-only` (if available) to correlate repeated `RED` runs or amendments with transcript friction.

## 2. Three Audit Lenses

Evaluate each lens independently:

1. **Tautological / Redundant Tests:**
   - Do tests exercise real invariants, persistence/migrations, failure paths, and concurrency (`-race`), or do they merely test trivial getters, mock return values, or duplicate existing coverage?
   - Flag any test that passes even if the production logic under test is inverted or deleted.
2. **Wasted Tool-Call Loops:**
   - Count repeated identical failures (same command failing ≥ 2 times without diagnosis), sequential single-tool turns where reads/edits could be batched, and redundant re-reads of unchanged files.
   - Note missing skill/doc hints that caused the agent to guess flags, state transitions, or paths.
3. **Architecture & Boundary Drift:**
   - Verify the Dependency Rule (`internal/domain` has zero external deps; `internal/store` depends only on `domain` + `sqlite`).
   - Flag shallow pass-through wrappers, duplicated logic across adapters, functions exceeding cyclomatic complexity 10, or undocumented config env vars.

## 3. Compact Audit Scorecard Output

Produce a scannable scorecard compatible with [merge-risk](../merge-risk/SKILL.md):

```markdown
### 🔍 Post-Slice Audit: `S-NN` — `<🟢 CLEAN | 🟡 MINOR FRICTION | 🔴 NEEDS FOLLOW-UP>`

| Lens | Status | Key Observation |
|---|:---:|---|
| 🧪 **Test Substance** | <🟢\|🟡\|🔴> | <≤ 12 words: real invariant vs tautological coverage> |
| 🔁 **Execution Efficiency** | <🟢\|🟡\|🔴> | <≤ 12 words: batching, repeated failures, wasted loops> |
| 🧱 **Boundary & Complexity** | <🟢\|🟡\|🔴> | <≤ 12 words: Dependency Rule, complexity ≤ 10, DRY> |

#### Proposed `## Gotchas` Updates
- `<skill-name>`: <1-sentence concrete rule preventing observed friction>
```

## Gotchas

- **State machine transitions before `ready_for_human`:** Slices in `claimed` must transition through `green` (or `in_review`) before `ready_for_human`; jumping directly from `claimed` → `ready_for_human` fails `ValidTransition`.
- **Loopback HTTP tests in sandboxed runners:** Tests using `httptest.NewServer` (`127.0.0.1`) fail inside network-isolated sandboxes with `connect: operation not permitted`; run `./scripts/check.sh` once outside the sandbox rather than retrying failing subpackages individually.
