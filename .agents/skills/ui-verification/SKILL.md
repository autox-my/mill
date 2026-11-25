---
name: ui-verification
description: Verify internal/webui changes end-to-end by booting mill serve on an isolated test DB, driving the UI with Playwright or Chrome DevTools, checking zero console/network errors, attaching screenshots to UI PRs, and updating Gotchas from mill improve --report-only.
---

# UI Verification (`ui-verification`)

Use this workflow whenever modifying `internal/webui` (`index.html`, `app.js`, `app.css`, `webui.go`) or API fields rendered by the dashboard.

## 1. Boot an Isolated Server & Seed Representative Data

Never test against the production database. Use a temporary DB and a throwaway `MILL_API_TOKEN`:

```sh
TMP_DB=$(mktemp -u /tmp/mill-ui-XXXXXX.db)
export MILL_API_TOKEN="ui-verify-token"
go run ./cmd/mill --db "$TMP_DB" init

# Seed low, medium, and high risk slices in ready_for_human + active lanes
go run ./cmd/mill --db "$TMP_DB" slice create --id S-91 --repo autox-my/mill --title "Low risk item"
go run ./cmd/mill --db "$TMP_DB" slice claim --id S-91 --owner verify@mac
go run ./cmd/mill --db "$TMP_DB" slice update --id S-91 --state green --verdict GREEN
go run ./cmd/mill --db "$TMP_DB" slice update --id S-91 --state ready_for_human --verdict GREEN --risk low

go run ./cmd/mill --db "$TMP_DB" slice create --id S-92 --repo autox-my/mill --title "High risk item"
go run ./cmd/mill --db "$TMP_DB" slice claim --id S-92 --owner verify@mac
go run ./cmd/mill --db "$TMP_DB" run record --slice S-92 --harness verify --phase verify --verdict GREEN --findings 1
go run ./cmd/mill --db "$TMP_DB" slice update --id S-92 --state green --verdict GREEN
go run ./cmd/mill --db "$TMP_DB" slice update --id S-92 --state ready_for_human --verdict GREEN --risk low

go run ./cmd/mill --db "$TMP_DB" serve --addr 127.0.0.1:18080
```

## 2. Drive the UI & Assert Zero Errors

Using **Playwright (`playwright-cli`)** or **Chrome DevTools MCP**:

1. Open `http://127.0.0.1:18080/` and sign in via the login form (`ui-verify-token`).
2. Verify the **Board** view:
   - Cards display risk badges (`low risk`, `medium risk`, `high risk`) and verdict badges.
   - `high`-risk cards are visually highlighted (red accent border/tint).
   - **Ready for human** sorts cards descending by risk (`high` → `medium` → `low`).
3. Click a card to open the **Slice Detail `<dialog>` drawer** and confirm `Merge risk` and the run timeline render cleanly.
4. Switch between **Board**, **Ledger**, **Hotspots**, and toggle **Theme** (light/dark).
5. Assert **zero** uncaught console errors, **zero** `4xx`/`5xx` API responses (other than the initial unauthenticated `GET /auth/session` `401` before login), and clean mobile + desktop layouts.
6. Capture screenshots (Board + Drawer) and attach or link them in the PR description.

## 3. Feed Observer Findings Back into Gotchas

Before wrapping up, inspect `mill improve --report-only` on the local or live ledger to catch recurring UI/verification failure patterns and record concrete prevention notes below.

## Gotchas

- **Initial `401` on `/auth/session`:** On first load without a session cookie, `boot()` calls `GET /auth/session` which intentionally returns HTTP `401` to trigger `showLogin()`. Do not treat that pre-login `401` as a runtime regression; verify zero `4xx`/`5xx` responses *after* login.
- **Unauthenticated static shell:** `internal/webui` embeds `static/*` via `//go:embed static/*`. Always rebuild the binary (or restart `go run`) after editing `index.html`, `app.js`, or `app.css`, and test with cache disabled so stale assets do not mask regressions.
- **SameSite=Strict & HTTP local testing:** `handleAuthLogin` sets `SameSite=Strict` and `Secure` only when TLS / `X-Forwarded-Proto: https` is present, so `http://127.0.0.1:18080` works in local browsers without HTTPS.
- **Card sorting & DOM order:** `Ready for human` sorting happens client-side in `renderBoard()` via `riskRank(b.risk) - riskRank(a.risk)` while preserving server creation order for ties.
