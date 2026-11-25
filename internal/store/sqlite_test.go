package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/autox-my/mill/internal/domain"
	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(%s): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedSlice(t *testing.T, st *Store, id string) {
	t.Helper()
	err := st.CreateSlice(context.Background(), domain.Slice{
		ID:     id,
		Repo:   "autox-my/mill",
		Title:  "Test slice " + id,
		Claims: []string{"REQ-0001"},
		Branch: "feature/" + id,
	})
	if err != nil {
		t.Fatalf("CreateSlice(%s): %v", id, err)
	}
}

// Proves REQ-0001: claiming a slice with an active unexpired lease held by another owner fails without modifying the lease.
func TestStore_ClaimExclusiveLease(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-01")
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	const workers = 10
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     []string
		rejected int
	)

	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			owner := fmt.Sprintf("worker-%d", idx)
			_, err := st.ClaimSlice(context.Background(), "S-01", owner, 30*time.Minute, now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins = append(wins, owner)
			case errors.Is(err, domain.ErrAlreadyClaimed):
				rejected++
			default:
				t.Errorf("worker-%d unexpected error: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	if len(wins) != 1 || rejected != workers-1 {
		t.Fatalf("got wins=%v rejected=%d, want 1 win and %d rejections", wins, rejected, workers-1)
	}

	got, err := st.GetSlice(context.Background(), "S-01")
	if err != nil {
		t.Fatalf("GetSlice: %v", err)
	}
	if got.Owner != wins[0] || got.Attempt != 1 {
		t.Fatalf("got owner=%q attempt=%d, want owner=%q attempt=1", got.Owner, got.Attempt, wins[0])
	}
}

// Proves REQ-0002: claiming a slice whose prior lease has expired succeeds and assigns the lease to the new owner.
func TestStore_ClaimExpiredLeaseTakeover(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-02")
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	first, err := st.ClaimSlice(context.Background(), "S-02", "claude-code@mac", 10*time.Minute, t0)
	if err != nil {
		t.Fatalf("first ClaimSlice: %v", err)
	}
	if first.Owner != "claude-code@mac" || first.Attempt != 1 {
		t.Fatalf("unexpected first claim: %+v", first)
	}

	// Still active at t0 + 5m: another owner must be rejected.
	_, err = st.ClaimSlice(context.Background(), "S-02", "opencode@ct120", 10*time.Minute, t0.Add(5*time.Minute))
	if !errors.Is(err, domain.ErrAlreadyClaimed) {
		t.Fatalf("expected ErrAlreadyClaimed before expiry, got %v", err)
	}

	// Expired at t0 + 11m: new owner takes over cleanly.
	second, err := st.ClaimSlice(context.Background(), "S-02", "opencode@ct120", 15*time.Minute, t0.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("takeover ClaimSlice: %v", err)
	}
	if second.Owner != "opencode@ct120" || second.Attempt != 2 {
		t.Fatalf("unexpected takeover slice: %+v", second)
	}
}

// Proves REQ-0003: renewing a slice lease succeeds only when requested by the current lease owner.
func TestStore_RenewLeaseOwnerOnly(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-03")
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	// Renewing an unowned slice fails.
	if _, err := st.RenewLease(context.Background(), "S-03", "claude-code@mac", 30*time.Minute, t0); !errors.Is(err, domain.ErrNotLeaseOwner) {
		t.Fatalf("unowned RenewLease got %v, want ErrNotLeaseOwner", err)
	}

	if _, err := st.ClaimSlice(context.Background(), "S-03", "claude-code@mac", 20*time.Minute, t0); err != nil {
		t.Fatalf("ClaimSlice: %v", err)
	}

	// Non-owner cannot renew.
	if _, err := st.RenewLease(context.Background(), "S-03", "antigravity@mac", 30*time.Minute, t0.Add(5*time.Minute)); !errors.Is(err, domain.ErrNotLeaseOwner) {
		t.Fatalf("non-owner RenewLease got %v, want ErrNotLeaseOwner", err)
	}

	// Owner can renew while active.
	renewed, err := st.RenewLease(context.Background(), "S-03", "claude-code@mac", 30*time.Minute, t0.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("owner RenewLease: %v", err)
	}
	wantExp := t0.Add(35 * time.Minute)
	if renewed.LeaseExpiresAt == nil || !renewed.LeaseExpiresAt.Equal(wantExp) {
		t.Fatalf("got expiry %v, want %v", renewed.LeaseExpiresAt, wantExp)
	}

	// Expired lease cannot be renewed (must re-claim).
	if _, err := st.RenewLease(context.Background(), "S-03", "claude-code@mac", 30*time.Minute, wantExp.Add(time.Minute)); !errors.Is(err, domain.ErrNotLeaseOwner) {
		t.Fatalf("expired RenewLease got %v, want ErrNotLeaseOwner", err)
	}
}

// Proves REQ-0004: updating a slice state rejects invalid state transitions.
func TestStore_UpdateSliceStateValidation(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-04")
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	// Cannot jump directly from spec to merged.
	if _, err := st.UpdateSliceState(context.Background(), "S-04", domain.StateMerged, domain.VerdictGreen, "", now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("spec->merged got %v, want ErrInvalidTransition", err)
	}

	if _, err := st.ClaimSlice(context.Background(), "S-04", "antigravity@mac", 30*time.Minute, now); err != nil {
		t.Fatalf("ClaimSlice: %v", err)
	}
	sl, err := st.UpdateSliceState(context.Background(), "S-04", domain.StateRed, domain.VerdictRed, "", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("claimed->red: %v", err)
	}
	if sl.State != domain.StateRed || sl.LastVerdict != domain.VerdictRed {
		t.Fatalf("unexpected slice after red: %+v", sl)
	}

	sl, err = st.UpdateSliceState(context.Background(), "S-04", domain.StateGreen, domain.VerdictGreen, "", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("red->green: %v", err)
	}
	if sl.State != domain.StateGreen || sl.LastVerdict != domain.VerdictGreen {
		t.Fatalf("unexpected slice after green: %+v", sl)
	}
}

// Proves REQ-0005: recording a run event persists the event in the run ledger and returns it when queried by slice ID.
func TestStore_RunLedgerAndStats(t *testing.T) {
	st := openTestStore(t)
	seedSlice(t, st, "S-05")
	seedSlice(t, st, "S-06")

	// Missing slice must fail with ErrNotFound.
	_, err := st.RecordRun(context.Background(), domain.RunEvent{
		SliceID: "MISSING",
		Harness: "claude-code",
		Phase:   "red",
		Verdict: domain.VerdictRed,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing slice RecordRun got %v, want ErrNotFound", err)
	}

	ev1, err := st.RecordRun(context.Background(), domain.RunEvent{
		SliceID:           "S-05",
		Harness:           "claude-code",
		Model:             "claude-sonnet",
		Phase:             "red",
		Verdict:           domain.VerdictRed,
		DurationSec:       15,
		Amendments:        1,
		FootprintFindings: 2,
		ReqDiffDirection:  "none",
		Notes:             "initial red test",
	})
	if err != nil || ev1.ID == 0 {
		t.Fatalf("RecordRun ev1: %v %+v", err, ev1)
	}

	_, err = st.RecordRun(context.Background(), domain.RunEvent{
		SliceID:     "S-05",
		Harness:     "antigravity",
		Phase:       "green",
		Verdict:     domain.VerdictGreen,
		DurationSec: 30,
	})
	if err != nil {
		t.Fatalf("RecordRun ev2: %v", err)
	}

	_, err = st.RecordRun(context.Background(), domain.RunEvent{
		SliceID: "S-06",
		Harness: "opencode",
		Phase:   "green",
		Verdict: domain.VerdictGreen,
	})
	if err != nil {
		t.Fatalf("RecordRun ev3: %v", err)
	}

	s05Runs, err := st.ListRuns(context.Background(), "S-05", 10)
	if err != nil {
		t.Fatalf("ListRuns(S-05): %v", err)
	}
	if len(s05Runs) != 2 {
		t.Fatalf("got %d runs for S-05, want 2", len(s05Runs))
	}

	summary, err := st.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if summary.TotalSlices != 2 || summary.TotalRuns != 3 || summary.GreenRuns != 2 || summary.RedRuns != 1 || summary.TotalAmendments != 1 || summary.TotalFindings != 2 {
		t.Fatalf("unexpected StatsSummary: %+v", summary)
	}
}

// Proves REQ-0012: store idempotently migrates slices.risk and enforces deterministic risk floor.
func TestStore_MergeRiskFloorAndMigration(t *testing.T) {
	// 1. Legacy database without risk column migrates idempotently to default 'low'.
	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	rawDB, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatalf("sql.Open legacy: %v", err)
	}
	const legacySchema = `
CREATE TABLE slices (
	id TEXT PRIMARY KEY,
	repo TEXT NOT NULL,
	title TEXT NOT NULL,
	claims_json TEXT NOT NULL DEFAULT '[]',
	intent_lock TEXT NOT NULL DEFAULT '',
	branch TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	lease_expires_at TEXT,
	attempt INTEGER NOT NULL DEFAULT 0,
	last_verdict TEXT NOT NULL DEFAULT 'pending',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);`
	if _, err := rawDB.Exec(legacySchema); err != nil {
		t.Fatalf("exec legacy schema: %v", err)
	}
	nowStr := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := rawDB.Exec(
		`INSERT INTO slices (id, repo, title, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"S-OLD", "autox-my/mill", "Legacy slice", "spec", nowStr, nowStr,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	_ = rawDB.Close()

	st1, err := Open(legacyPath)
	if err != nil {
		t.Fatalf("first Open(legacy): %v", err)
	}
	gotLegacy, err := st1.GetSlice(context.Background(), "S-OLD")
	if err != nil || gotLegacy.Risk != domain.RiskLow {
		t.Fatalf("legacy slice risk = %q (err=%v), want %q", gotLegacy.Risk, err, domain.RiskLow)
	}
	_ = st1.Close()

	// Re-opening must be idempotent.
	st2, err := Open(legacyPath)
	if err != nil {
		t.Fatalf("second Open(legacy): %v", err)
	}
	_ = st2.Close()

	// 2. Fresh store: default risk, validation, and deterministic floor clamping.
	st := openTestStore(t)
	seedSlice(t, st, "S-RISK")
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	ctx := context.Background()

	sl, err := st.GetSlice(ctx, "S-RISK")
	if err != nil || sl.Risk != domain.RiskLow {
		t.Fatalf("new slice risk = %q (err=%v), want low", sl.Risk, err)
	}

	if _, err := st.UpdateSliceState(ctx, "S-RISK", "", "", "", now); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty state and risk got %v, want ErrInvalidInput", err)
	}
	if _, err := st.UpdateSliceState(ctx, "S-RISK", "", "", "extreme", now); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid risk got %v, want ErrInvalidInput", err)
	}

	// Risk-only update succeeds without state transition.
	sl, err = st.UpdateSliceState(ctx, "S-RISK", "", "", domain.RiskMedium, now)
	if err != nil || sl.Risk != domain.RiskMedium || sl.State != domain.StateSpec {
		t.Fatalf("risk-only update got (%+v, %v), want state=spec risk=medium", sl, err)
	}

	// Lowering back to low succeeds when no run floor blocks it.
	sl, err = st.UpdateSliceState(ctx, "S-RISK", "", "", domain.RiskLow, now.Add(time.Minute))
	if err != nil || sl.Risk != domain.RiskLow {
		t.Fatalf("lower risk without floor got (%+v, %v), want low", sl, err)
	}

	// Amendments > 0 raises floor to medium.
	if _, err := st.RecordRun(ctx, domain.RunEvent{
		SliceID:    "S-RISK",
		Harness:    "codex",
		Phase:      "verify",
		Verdict:    domain.VerdictGreen,
		Amendments: 1,
	}); err != nil {
		t.Fatalf("RecordRun amendments: %v", err)
	}
	sl, err = st.GetSlice(ctx, "S-RISK")
	if err != nil || sl.Risk != domain.RiskMedium {
		t.Fatalf("after amendments risk = %q (err=%v), want medium", sl.Risk, err)
	}
	// Attempting to lower to low is clamped to medium.
	sl, err = st.UpdateSliceState(ctx, "S-RISK", "", "", domain.RiskLow, now.Add(2*time.Minute))
	if err != nil || sl.Risk != domain.RiskMedium {
		t.Fatalf("clamped risk = %q (err=%v), want medium", sl.Risk, err)
	}
	// Raising above floor to high is allowed.
	sl, err = st.UpdateSliceState(ctx, "S-RISK", "", "", domain.RiskHigh, now.Add(3*time.Minute))
	if err != nil || sl.Risk != domain.RiskHigh {
		t.Fatalf("raise to high got (%+v, %v), want high", sl, err)
	}

	// Footprint findings or breaking/loosening req-diff enforces high floor.
	seedSlice(t, st, "S-HIGH1")
	if _, err := st.RecordRun(ctx, domain.RunEvent{
		SliceID:           "S-HIGH1",
		Harness:           "codex",
		Phase:             "verify",
		Verdict:           domain.VerdictGreen,
		FootprintFindings: 1,
	}); err != nil {
		t.Fatalf("RecordRun findings: %v", err)
	}
	if sl, err := st.UpdateSliceState(ctx, "S-HIGH1", "", "", domain.RiskLow, now); err != nil || sl.Risk != domain.RiskHigh {
		t.Fatalf("findings floor clamped = %q (err=%v), want high", sl.Risk, err)
	}

	seedSlice(t, st, "S-HIGH2")
	if _, err := st.RecordRun(ctx, domain.RunEvent{
		SliceID:          "S-HIGH2",
		Harness:          "codex",
		Phase:            "verify",
		Verdict:          domain.VerdictGreen,
		ReqDiffDirection: "breaking",
	}); err != nil {
		t.Fatalf("RecordRun breaking: %v", err)
	}
	if sl, err := st.UpdateSliceState(ctx, "S-HIGH2", "", "", domain.RiskMedium, now); err != nil || sl.Risk != domain.RiskHigh {
		t.Fatalf("breaking floor clamped = %q (err=%v), want high", sl.Risk, err)
	}
}

// Proves REQ-0015: store idempotently migrates run_events telemetry columns and aggregates cycle time and breakdowns.
func TestStore_RunTelemetryMigrationAndStats(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy-telemetry.db")
	rawDB, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatalf("sql.Open legacy: %v", err)
	}
	const legacySchema = `
CREATE TABLE slices (
	id TEXT PRIMARY KEY,
	repo TEXT NOT NULL,
	title TEXT NOT NULL,
	claims_json TEXT NOT NULL DEFAULT '[]',
	intent_lock TEXT NOT NULL DEFAULT '',
	branch TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	lease_expires_at TEXT,
	attempt INTEGER NOT NULL DEFAULT 0,
	last_verdict TEXT NOT NULL DEFAULT 'pending',
	risk TEXT NOT NULL DEFAULT 'low',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE run_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slice_id TEXT NOT NULL REFERENCES slices(id) ON DELETE CASCADE,
	harness TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	phase TEXT NOT NULL,
	verdict TEXT NOT NULL,
	duration_sec INTEGER NOT NULL DEFAULT 0,
	amendments INTEGER NOT NULL DEFAULT 0,
	footprint_findings INTEGER NOT NULL DEFAULT 0,
	req_diff_direction TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);`
	if _, err := rawDB.Exec(legacySchema); err != nil {
		t.Fatalf("exec legacy schema: %v", err)
	}
	t0 := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	if _, err := rawDB.Exec(
		`INSERT INTO slices (id, repo, title, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"S-LEG", "autox-my/mill", "Legacy telemetry slice", "merged", t0.Format(time.RFC3339Nano), t0.Add(120*time.Second).Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("insert legacy slice: %v", err)
	}
	if _, err := rawDB.Exec(
		`INSERT INTO run_events (slice_id, harness, model, phase, verdict, duration_sec, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"S-LEG", "claude-code", "sonnet-4", "verify", "GREEN", 25, t0.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("insert legacy run: %v", err)
	}
	_ = rawDB.Close()

	// Opening legacy DB migrates run_events; re-opening must be idempotent.
	st1, err := Open(legacyPath)
	if err != nil {
		t.Fatalf("first Open(legacy): %v", err)
	}
	_ = st1.Close()

	st, err := Open(legacyPath)
	if err != nil {
		t.Fatalf("second Open(legacy): %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	runs, err := st.ListRuns(ctx, "S-LEG", 10)
	if err != nil || len(runs) != 1 || runs[0].TokensIn != 0 || runs[0].CostUSD != 0 {
		t.Fatalf("legacy run list = (%+v, %v)", runs, err)
	}

	// Create second merged slice and record telemetry-enriched runs.
	if err := st.CreateSlice(ctx, domain.Slice{
		ID: "S-NEW", Repo: "autox-my/mill", Title: "Telemetry slice",
		State: domain.StateMerged, CreatedAt: t0, UpdatedAt: t0.Add(240 * time.Second),
	}); err != nil {
		t.Fatalf("CreateSlice S-NEW: %v", err)
	}
	ev, err := st.RecordRun(ctx, domain.RunEvent{
		SliceID:     "S-NEW",
		Harness:     "antigravity",
		Model:       "gemini-4-neon",
		Phase:       "verify",
		Verdict:     domain.VerdictGreen,
		DurationSec: 45,
		TokensIn:    1500,
		TokensOut:   500,
		CostUSD:     0.12,
	})
	if err != nil || ev.TokensIn != 1500 || ev.TokensOut != 500 || ev.CostUSD != 0.12 {
		t.Fatalf("RecordRun telemetry = (%+v, %v)", ev, err)
	}

	runs, err = st.ListRuns(ctx, "S-NEW", 10)
	if err != nil || len(runs) != 1 || runs[0].TokensIn != 1500 || runs[0].TokensOut != 500 || runs[0].CostUSD != 0.12 {
		t.Fatalf("ListRuns telemetry = (%+v, %v)", runs, err)
	}

	summary, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if summary.TotalTokensIn != 1500 || summary.TotalTokensOut != 500 || summary.TotalCostUSD < 0.119 || summary.TotalCostUSD > 0.121 {
		t.Fatalf("unexpected token/cost totals: %+v", summary)
	}
	if summary.CycleTime.MergedCount != 2 || summary.CycleTime.AvgSec != 180 || summary.CycleTime.P50Sec != 180 || summary.CycleTime.MaxSec != 240 {
		t.Fatalf("unexpected CycleTime: %+v", summary.CycleTime)
	}
	if summary.AvgCostPerMergedUSD < 0.059 || summary.AvgCostPerMergedUSD > 0.061 || summary.AvgTokensPerMerged != 1000 {
		t.Fatalf("unexpected per-merged averages: cost=%v tokens=%v", summary.AvgCostPerMergedUSD, summary.AvgTokensPerMerged)
	}
	if len(summary.ByModel) != 2 || len(summary.ByHarness) != 2 {
		t.Fatalf("unexpected breakdowns: models=%+v harnesses=%+v", summary.ByModel, summary.ByHarness)
	}
}
