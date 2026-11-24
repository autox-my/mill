package httpclient

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/httpapi"
	"github.com/autox-my/mill/internal/store"
)

func TestHTTPClient_RoundTripAndErrors(t *testing.T) {
	if _, err := New("", "tok"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty url got %v, want ErrInvalidInput", err)
	}
	if _, err := New("ftp://example.com", "tok"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("bad scheme got %v, want ErrInvalidInput", err)
	}
	if _, err := New("https://mill.example.ts.net", ""); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty token got %v, want ErrInvalidInput", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	srv := httptest.NewServer(httpapi.NewHandler(st, "test-tok"))
	defer srv.Close()

	c, err := New(srv.URL, "test-tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	err = c.CreateSlice(ctx, domain.Slice{
		ID:     "S-20",
		Repo:   "autox-my/mill",
		Title:  "Remote HTTP client",
		Claims: []string{"REQ-0010"},
		Branch: "feature/s-20",
	})
	if err != nil {
		t.Fatalf("CreateSlice: %v", err)
	}
	if err := c.CreateSlice(ctx, domain.Slice{ID: "S-20", Repo: "autox-my/mill", Title: "dup"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate CreateSlice got %v, want ErrAlreadyExists", err)
	}

	got, err := c.GetSlice(ctx, "S-20")
	if err != nil || got.ID != "S-20" {
		t.Fatalf("GetSlice: err=%v got=%+v", err, got)
	}
	if _, err := c.GetSlice(ctx, "MISSING"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing GetSlice got %v, want ErrNotFound", err)
	}

	list, err := c.ListSlices(ctx, "spec")
	if err != nil || len(list) != 1 || list[0].ID != "S-20" {
		t.Fatalf("ListSlices: err=%v list=%+v", err, list)
	}

	now := time.Now().UTC()
	claimed, err := c.ClaimSlice(ctx, "S-20", "claude@mac", 20*time.Minute, now)
	if err != nil || claimed.Owner != "claude@mac" {
		t.Fatalf("ClaimSlice: err=%v claimed=%+v", err, claimed)
	}
	if _, err := c.ClaimSlice(ctx, "S-20", "opencode@ct120", 20*time.Minute, now); !errors.Is(err, domain.ErrAlreadyClaimed) {
		t.Fatalf("competing ClaimSlice got %v, want ErrAlreadyClaimed", err)
	}

	if _, err := c.RenewLease(ctx, "S-20", "other@mac", 20*time.Minute, now); !errors.Is(err, domain.ErrNotLeaseOwner) {
		t.Fatalf("non-owner RenewLease got %v, want ErrNotLeaseOwner", err)
	}
	renewed, err := c.RenewLease(ctx, "S-20", "claude@mac", 30*time.Minute, now)
	if err != nil || renewed.LeaseExpiresAt == nil {
		t.Fatalf("RenewLease: err=%v renewed=%+v", err, renewed)
	}

	if _, err := c.UpdateSliceState(ctx, "S-20", domain.StateMerged, domain.VerdictGreen, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("invalid transition got %v, want ErrInvalidTransition", err)
	}
	updated, err := c.UpdateSliceState(ctx, "S-20", domain.StateGreen, domain.VerdictGreen, now)
	if err != nil || updated.State != domain.StateGreen {
		t.Fatalf("UpdateSliceState: err=%v updated=%+v", err, updated)
	}

	ev, err := c.RecordRun(ctx, domain.RunEvent{
		SliceID: "S-20",
		Harness: "claude-code",
		Phase:   "green",
		Verdict: domain.VerdictGreen,
	})
	if err != nil || ev.ID == 0 {
		t.Fatalf("RecordRun: err=%v ev=%+v", err, ev)
	}

	runs, err := c.ListRuns(ctx, "S-20", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: err=%v runs=%+v", err, runs)
	}

	stats, err := c.Stats(ctx)
	if err != nil || stats.TotalSlices != 1 || stats.GreenRuns != 1 {
		t.Fatalf("Stats: err=%v stats=%+v", err, stats)
	}

	badClient, err := New(srv.URL, "wrong-tok")
	if err != nil {
		t.Fatalf("New badClient: %v", err)
	}
	if _, err := badClient.Stats(ctx); err == nil {
		t.Fatal("expected unauthorized error with wrong token")
	}
}
