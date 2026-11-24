package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/autox-my/mill/internal/domain"
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
	if _, err := st.UpdateSliceState(context.Background(), "S-04", domain.StateMerged, domain.VerdictGreen, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("spec->merged got %v, want ErrInvalidTransition", err)
	}

	if _, err := st.ClaimSlice(context.Background(), "S-04", "antigravity@mac", 30*time.Minute, now); err != nil {
		t.Fatalf("ClaimSlice: %v", err)
	}
	sl, err := st.UpdateSliceState(context.Background(), "S-04", domain.StateRed, domain.VerdictRed, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("claimed->red: %v", err)
	}
	if sl.State != domain.StateRed || sl.LastVerdict != domain.VerdictRed {
		t.Fatalf("unexpected slice after red: %+v", sl)
	}

	sl, err = st.UpdateSliceState(context.Background(), "S-04", domain.StateGreen, domain.VerdictGreen, now.Add(2*time.Minute))
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
