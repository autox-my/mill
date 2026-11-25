---
name: merge-risk
description: Run a pre-PR 5-dimension merge-risk assessment (Alignment, Impact, Verification, Rollback cost, Error detection) before opening a PR on mill, record the risk level on the slice, and embed the summary in the PR description.
---

# Pre-PR Merge Risk Assessment (`merge-risk`)

Run this 5-dimension checklist **before opening a PR** so gaps are fixed early and the human reviewer sees an honest, evidence-backed risk signal in both the PR body and the `mill` board (`Ready for human` sorts `high` risk cards first).

## 1. The 5 Dimensions

Evaluate every dimension against the actual `git diff main...HEAD` and gate output:

1. **Alignment:** Does the diff stay strictly within the slice's IntentGate footprint and `claims/REQ-*.yml` obligations? Are package boundaries (`domain` zero external deps, `store` only `domain` + `sqlite`) preserved?
2. **Impact:** What is the blast radius across layers (`domain`, `store` schema/migration, `httpapi`, `httpclient`, `mcp`, `webui`, `cmd/mill`, live CT `122`)? Are backwards-compatibility and idempotency handled?
3. **Verification:** Did `./scripts/check.sh` (or `go test -race -cover`, `go vet`, `req canon --fail-on-flag`, `req check`) pass after the **final** edit? Are all packages ≥ 70% coverage? Were UI changes visually verified?
4. **Rollback cost:** If this PR causes a regression after merge, how easy is it to revert (`git revert`, additive SQLite `ALTER TABLE` vs destructive migration, binary redeploy)?
5. **Error detection:** Do tests cover negative/error paths, boundary inputs, and race conditions (`-race`), or could failures happen silently at runtime?

## 2. Classification Rules & Deterministic Floor

Assign `low`, `medium`, or `high`:

- **`low`:** Additive or localized change, full test/gate pass, idempotent or no schema change, trivial rollback, strong negative-path coverage.
- **`medium`:** Cross-layer change, schema/API contract addition, or any run with `amendments > 0`, `RED`, or `UNDECIDABLE` verdict.
- **`high`:** Any `footprint_findings > 0`, `req_diff_direction` in (`breaking`, `loosening`), auth/security-sensitive change, destructive migration, or unverified runtime path.

> **Deterministic floor (`mill` invariant):** `mill` automatically clamps `slice.risk` in SQLite (`RecordRun` and `UpdateSliceState`) to at least the floor implied by run events:
> - `footprint_findings > 0` or `req_diff_direction` in (`breaking`, `loosening`) → floor **`high`**
> - `amendments > 0` or `verdict` in (`RED`, `UNDECIDABLE`) → floor **`medium`**
>
> Attempting to set `--risk low` when the floor is higher is clamped server-side.

## 3. Record & Paste into PR

1. Update the slice risk on the board:
   ```sh
   mill slice update --id S-NN --risk low|medium|high
   ```
2. Paste this **visual, 5-second-scannable** block into the PR description (never dump prose paragraphs or comma-separated test lists into the main view):
   - Use `🟢 LOW` + `[!TIP]`, `🟡 MEDIUM` + `[!WARNING]`, or `🔴 HIGH` + `[!CAUTION]`.
   - Keep every table cell ≤ 12 words so the human's eye jumps straight to non-green rows.
   - Include a compact **Mermaid diagram** (≤ 6 nodes) for any `MEDIUM` or `HIGH` PR showing blast radius & trust boundaries (omit for trivial `LOW` docs PRs).
   - List **at most 3 files** under **Where to Focus Review** pointing directly at the riskiest code.
   - Put per-package coverage numbers and test names inside the collapsible `<details>` block.

   ```markdown
   ### <🟢|🟡|🔴> Merge Risk: `<LOW|MEDIUM|HIGH>`

   > [!<TIP|WARNING|CAUTION>]
   > **Why `<LOW|MEDIUM|HIGH>`:** <1-sentence explanation of the specific risk driver(s).>

   | Dimension | Level | Key Signal |
   |---|:---:|---|
   | 🧭 **Alignment** | <🟢\|🟡\|🔴> | <≤ 12 words: footprint & Dependency Rule status> |
   | 💥 **Impact** | <🟢\|🟡\|🔴> | <≤ 12 words: blast radius, routes, schema/migration> |
   | ✅ **Verification** | <🟢\|🟡\|🔴> | <≤ 12 words: check.sh, test count, claims, min cov> |
   | ⏪ **Rollback** | <🟢\|🟡\|🔴> | <≤ 12 words: git revert / additive vs destructive> |
   | 🩺 **Error Detection** | <🟢\|🟡\|🔴> | <≤ 12 words: key negative/edge/race test coverage> |

   #### Blast Radius & Trust Boundaries
   ```mermaid
   flowchart LR
     A["Caller / Entrypoint"] -->|"auth / contract"| B["Changed Component<br/>(NEW or MODIFIED)"]
     B -->|"persistence / side effect"| C[("SQLite / Store")]
   ```

   #### 👀 Where to Focus Review
   - **<Concern 1>:** `<path/to/file.go>` (<what to check>)
   - **<Concern 2>:** `<path/to/file.go>` (<what to check>)

   <details>
   <summary>📊 Detailed Verification & Coverage (click to expand)</summary>

   - `check.sh`: `gofmt`, `go vet`, `<N>` tests PASS (`-race`), `req canon` (`<M>` claims, 0 flags), `req gate` PASS
   - Coverage: `<pkg1> XX.X%` · `<pkg2> XX.X%` · ...
   - Verifier / UI check: <Argon verifier result / screenshot note>

   </details>
   ```
