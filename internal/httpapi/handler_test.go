package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/httpapi"
	"github.com/autox-my/mill/internal/store"
)

const testToken = "test-secret-token-value"

func newTestServer(t *testing.T, token string) (*store.Store, http.Handler) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, httpapi.NewHandler(st, token)
}

func doReq(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		switch v := body.(type) {
		case string:
			buf.WriteString(v)
		default:
			if err := json.NewEncoder(&buf).Encode(v); err != nil {
				t.Fatalf("encode request body: %v", err)
			}
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func authHeader(tok string) string {
	return "Bearer " + tok
}

func TestAuth_RejectsUnauthorizedAndEmptyConfig(t *testing.T) {
	// Empty configured token must fail safe (reject all requests).
	_, emptyCfgHandler := newTestServer(t, "")
	rec := doReq(t, emptyCfgHandler, http.MethodGet, "/slices", authHeader(testToken), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty server token: got status %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	// Configured token: missing header, wrong scheme, wrong token must return 401.
	_, h := newTestServer(t, testToken)
	cases := []struct {
		name string
		auth string
	}{
		{"missing header", ""},
		{"wrong scheme", "Basic " + testToken},
		{"wrong token", "Bearer wrong-token"},
		{"empty bearer", "Bearer "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doReq(t, h, http.MethodGet, "/slices", tc.auth, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: got status %d, want %d", tc.name, rec.Code, http.StatusUnauthorized)
			}
		})
	}

	// Valid token succeeds.
	rec = doReq(t, h, http.MethodGet, "/slices", authHeader(testToken), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: got status %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSlices_CreateAndListFilters(t *testing.T) {
	_, h := newTestServer(t, testToken)
	auth := authHeader(testToken)

	// Empty list returns [] rather than null.
	rec := doReq(t, h, http.MethodGet, "/slices", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list empty: got %d, want 200", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("list empty body = %q, want []", rec.Body.String())
	}

	// Create two slices in different repos.
	s1 := domain.Slice{ID: "SL-1", Repo: "repo-a", Title: "First slice", Claims: []string{"REQ-0001"}}
	s2 := domain.Slice{ID: "SL-2", Repo: "repo-b", Title: "Second slice", State: domain.StateLocked}

	rec = doReq(t, h, http.MethodPost, "/slices", auth, s1)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create s1: got %d (%s), want 201", rec.Code, rec.Body.String())
	}
	var created domain.Slice
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode created slice: %v", err)
	}
	if created.ID != "SL-1" || created.State != domain.StateSpec {
		t.Fatalf("unexpected created slice: %+v", created)
	}

	rec = doReq(t, h, http.MethodGet, "/slices/SL-1", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get SL-1: got %d, want 200", rec.Code)
	}
	rec = doReq(t, h, http.MethodGet, "/slices/MISSING", auth, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get MISSING: got %d, want 404", rec.Code)
	}

	rec = doReq(t, h, http.MethodPost, "/slices", auth, s2)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create s2: got %d, want 201", rec.Code)
	}

	// Duplicate ID -> 409 Conflict.
	rec = doReq(t, h, http.MethodPost, "/slices", auth, s1)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d, want 409", rec.Code)
	}

	// Invalid input -> 400 Bad Request.
	rec = doReq(t, h, http.MethodPost, "/slices", auth, domain.Slice{ID: "SL-BAD"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid create: got %d, want 400", rec.Code)
	}

	// Malformed JSON -> 400 Bad Request.
	rec = doReq(t, h, http.MethodPost, "/slices", auth, "{bad json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: got %d, want 400", rec.Code)
	}

	// Filter by state.
	rec = doReq(t, h, http.MethodGet, "/slices?state=locked", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list state=locked: got %d, want 200", rec.Code)
	}
	var got []domain.Slice
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "SL-2" {
		t.Fatalf("state filter got %+v, want [SL-2]", got)
	}

	// Filter by repo.
	rec = doReq(t, h, http.MethodGet, "/slices?repo=repo-a", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list repo=repo-a: got %d, want 200", rec.Code)
	}
	got = nil
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "SL-1" {
		t.Fatalf("repo filter got %+v, want [SL-1]", got)
	}

	// Invalid state filter -> 400.
	rec = doReq(t, h, http.MethodGet, "/slices?state=bogus", auth, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid state filter: got %d, want 400", rec.Code)
	}
}

func TestSlices_ClaimRenewUpdateAndErrors(t *testing.T) {
	_, h := newTestServer(t, testToken)
	auth := authHeader(testToken)

	// Create slice.
	rec := doReq(t, h, http.MethodPost, "/slices", auth, domain.Slice{
		ID: "SL-10", Repo: "mill", Title: "HTTP API",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201", rec.Code)
	}

	// Claim non-existent -> 404.
	rec = doReq(t, h, http.MethodPost, "/slices/NOPE/claim", auth, map[string]any{
		"owner": "agent-a", "ttl_seconds": 60,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("claim missing: got %d, want 404", rec.Code)
	}

	// Claim invalid input -> 400.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-10/claim", auth, map[string]any{
		"owner": "", "ttl_seconds": 60,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("claim empty owner: got %d, want 400", rec.Code)
	}

	// Claim by agent-a -> 200.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-10/claim", auth, map[string]any{
		"owner": "agent-a", "ttl_seconds": 120,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim agent-a: got %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var claimed domain.Slice
	if err := json.NewDecoder(rec.Body).Decode(&claimed); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claimed.Owner != "agent-a" || claimed.State != domain.StateClaimed || claimed.Attempt != 1 {
		t.Fatalf("unexpected claimed slice: %+v", claimed)
	}

	// Claim by agent-b while active -> 409 Conflict.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-10/claim", auth, map[string]any{
		"owner": "agent-b", "ttl_seconds": 60,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("claim conflict: got %d, want 409", rec.Code)
	}

	// Renew by agent-b (not lease owner) -> 403 Forbidden.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-10/renew", auth, map[string]any{
		"owner": "agent-b", "ttl_seconds": 60,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("renew non-owner: got %d, want 403", rec.Code)
	}

	// Renew by agent-a -> 200.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-10/renew", auth, map[string]any{
		"owner": "agent-a", "ttl": "5m",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("renew owner: got %d (%s), want 200", rec.Code, rec.Body.String())
	}

	// Invalid transition (claimed -> merged) -> 409 Conflict.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-10/update", auth, map[string]any{
		"state": "merged", "verdict": "GREEN",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("invalid transition: got %d, want 409", rec.Code)
	}

	// Valid transition (claimed -> green) -> 200.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-10/update", auth, map[string]any{
		"state": "green", "verdict": "GREEN",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid update: got %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var updated domain.Slice
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.State != domain.StateGreen || updated.LastVerdict != domain.VerdictGreen {
		t.Fatalf("unexpected updated slice: %+v", updated)
	}
}

func TestRuns_AndStats(t *testing.T) {
	_, h := newTestServer(t, testToken)
	auth := authHeader(testToken)

	// Create slice first.
	rec := doReq(t, h, http.MethodPost, "/slices", auth, domain.Slice{
		ID: "SL-20", Repo: "mill", Title: "Ledger test",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201", rec.Code)
	}

	// Empty runs returns [].
	rec = doReq(t, h, http.MethodGet, "/runs", auth, nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty runs: got %d %q", rec.Code, rec.Body.String())
	}

	// Invalid limit query param -> 400.
	rec = doReq(t, h, http.MethodGet, "/runs?limit=abc", auth, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit: got %d, want 400", rec.Code)
	}

	// Record run for missing slice -> 404.
	rec = doReq(t, h, http.MethodPost, "/runs", auth, domain.RunEvent{
		SliceID: "MISSING", Harness: "codex", Phase: "green", Verdict: domain.VerdictGreen,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing slice run: got %d, want 404", rec.Code)
	}

	// Record valid runs.
	ev1 := domain.RunEvent{
		SliceID: "SL-20", Harness: "codex", Phase: "red", Verdict: domain.VerdictRed,
		DurationSec: 12, Amendments: 1, FootprintFindings: 2,
	}
	ev2 := domain.RunEvent{
		SliceID: "SL-20", Harness: "codex", Phase: "green", Verdict: domain.VerdictGreen,
		DurationSec: 30,
	}
	if rec = doReq(t, h, http.MethodPost, "/runs", auth, ev1); rec.Code != http.StatusCreated {
		t.Fatalf("post run 1: got %d (%s), want 201", rec.Code, rec.Body.String())
	}
	if rec = doReq(t, h, http.MethodPost, "/runs", auth, ev2); rec.Code != http.StatusCreated {
		t.Fatalf("post run 2: got %d (%s), want 201", rec.Code, rec.Body.String())
	}

	// List runs with slice_id and limit=1.
	rec = doReq(t, h, http.MethodGet, "/runs?slice_id=SL-20&limit=1", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get runs: got %d, want 200", rec.Code)
	}
	var runs []domain.RunEvent
	if err := json.NewDecoder(rec.Body).Decode(&runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Phase != "green" {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	// Stats endpoint.
	rec = doReq(t, h, http.MethodGet, "/stats", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get stats: got %d, want 200", rec.Code)
	}
	var stats domain.StatsSummary
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.TotalSlices != 1 || stats.TotalRuns != 2 || stats.GreenRuns != 1 || stats.RedRuns != 1 || stats.TotalAmendments != 1 || stats.TotalFindings != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
