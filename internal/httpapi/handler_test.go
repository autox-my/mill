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
	"github.com/autox-my/mill/internal/webhooks"
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
		SliceID: "SL-20", Harness: "codex", Model: "gpt-5", Phase: "green", Verdict: domain.VerdictGreen,
		DurationSec: 30, TokensIn: 1000, TokensOut: 250, CostUSD: 0.04,
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
	if len(runs) != 1 || runs[0].Phase != "green" || runs[0].TokensIn != 1000 || runs[0].TokensOut != 250 || runs[0].CostUSD != 0.04 {
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
	if stats.TotalSlices != 1 || stats.TotalRuns != 2 || stats.GreenRuns != 1 || stats.RedRuns != 1 || stats.TotalAmendments != 1 || stats.TotalFindings != 2 || stats.TotalTokensIn != 1000 || stats.TotalTokensOut != 250 || len(stats.ByModel) != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// Proves REQ-0011: browser sessions authenticate via constant-time cookie login and UI shell leaks no data.
func TestAuth_CookieLoginSessionAndLogout(t *testing.T) {
	uiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>mill shell</body></html>"))
	})
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer st.Close()

	h := httpapi.NewHandlerWithConfig(httpapi.Config{
		Store:     st,
		APIToken:  testToken,
		UIHandler: uiHandler,
	})

	// Unauthenticated GET / serves UI shell; /auth/session and /stats fail closed with 401.
	rec := doReq(t, h, http.MethodGet, "/", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mill shell") {
		t.Fatalf("GET / got %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, h, http.MethodGet, "/auth/session", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth /auth/session got %d, want 401", rec.Code)
	}
	if rec := doReq(t, h, http.MethodGet, "/stats", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth /stats got %d, want 401", rec.Code)
	}

	// Wrong token on /auth/login returns 401 and sets no cookie.
	rec = doReq(t, h, http.MethodPost, "/auth/login", "", map[string]string{"token": "wrong"})
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong login got %d cookies=%v", rec.Code, rec.Result().Cookies())
	}

	// Valid token returns 200 and sets HttpOnly SameSite=Strict cookie; cookie never equals raw token.
	rec = doReq(t, h, http.MethodPost, "/auth/login", "", map[string]string{"token": testToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid login got %d (%s)", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "mill_session" || !cookies[0].HttpOnly || cookies[0].Value == testToken {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}

	// Cookie grants access to /auth/session and /stats.
	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie /stats got %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie /auth/session got %d, want 200", rec.Code)
	}

	// Logout clears the cookie.
	rec = doReq(t, h, http.MethodPost, "/auth/logout", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout got %d", rec.Code)
	}
	cleared := rec.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge != -1 {
		t.Fatalf("expected cleared cookie, got %+v", cleared)
	}
}

// Proves REQ-0011: observer report endpoint requires auth and returns grounded hotspot metrics.
func TestObserverReport(t *testing.T) {
	_, h := newTestServer(t, testToken)
	auth := authHeader(testToken)

	// Unauthenticated /observer/report fails closed.
	if rec := doReq(t, h, http.MethodGet, "/observer/report", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth /observer/report got %d, want 401", rec.Code)
	}

	sl := domain.Slice{ID: "SL-30", Repo: "autox-my/mill", Title: "Observer test"}
	if rec := doReq(t, h, http.MethodPost, "/slices", auth, sl); rec.Code != http.StatusCreated {
		t.Fatalf("create slice: %d %s", rec.Code, rec.Body.String())
	}
	ev := domain.RunEvent{SliceID: "SL-30", Harness: "claude-code", Phase: "red", Verdict: domain.VerdictRed, Amendments: 2, FootprintFindings: 1}
	if rec := doReq(t, h, http.MethodPost, "/runs", auth, ev); rec.Code != http.StatusCreated {
		t.Fatalf("create run: %d %s", rec.Code, rec.Body.String())
	}

	rec := doReq(t, h, http.MethodGet, "/observer/report", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /observer/report got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"SL-30"`) || !strings.Contains(rec.Body.String(), `"red_events":1`) {
		t.Fatalf("unexpected observer report body: %s", rec.Body.String())
	}
}

// Proves REQ-0012: HTTP update endpoint accepts risk and enforces the deterministic run risk floor.
func TestUpdateSlice_RiskFloor(t *testing.T) {
	_, h := newTestServer(t, testToken)
	auth := authHeader(testToken)

	rec := doReq(t, h, http.MethodPost, "/slices", auth, domain.Slice{
		ID: "SL-RISK", Repo: "autox-my/mill", Title: "Risk HTTP test",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", rec.Code, rec.Body.String())
	}

	// Risk-only update succeeds.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-RISK/update", auth, map[string]any{
		"risk": "medium",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("risk-only update: got %d (%s)", rec.Code, rec.Body.String())
	}
	var sl domain.Slice
	if err := json.NewDecoder(rec.Body).Decode(&sl); err != nil {
		t.Fatalf("decode slice: %v", err)
	}
	if sl.Risk != domain.RiskMedium || sl.State != domain.StateSpec {
		t.Fatalf("unexpected slice after risk update: %+v", sl)
	}

	// Invalid risk returns 400.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-RISK/update", auth, map[string]any{
		"risk": "bogus",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid risk: got %d, want 400", rec.Code)
	}

	// Recording a run with footprint_findings=1 raises the floor to high.
	ev := domain.RunEvent{
		SliceID: "SL-RISK", Harness: "codex", Phase: "verify", Verdict: domain.VerdictGreen, FootprintFindings: 1,
	}
	if rec = doReq(t, h, http.MethodPost, "/runs", auth, ev); rec.Code != http.StatusCreated {
		t.Fatalf("record run: got %d (%s)", rec.Code, rec.Body.String())
	}

	// Attempting to lower risk to low is clamped to high.
	rec = doReq(t, h, http.MethodPost, "/slices/SL-RISK/update", auth, map[string]any{
		"risk": "low",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("clamped update: got %d (%s)", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&sl); err != nil {
		t.Fatalf("decode clamped slice: %v", err)
	}
	if sl.Risk != domain.RiskHigh {
		t.Fatalf("expected clamped risk=high, got %q", sl.Risk)
	}
}

// Proves REQ-0013: mounted POST /webhooks/{name} authenticates independently of MILL_API_TOKEN and deduplicates deliveries.
func TestWebhooks_MountedRoute(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer st.Close()
	whStore, err := webhooks.OpenDeliveryStore(":memory:")
	if err != nil {
		t.Fatalf("open delivery store: %v", err)
	}
	defer whStore.Close()

	h := httpapi.NewHandlerWithConfig(httpapi.Config{
		Store:    st,
		APIToken: testToken,
		WebhookHandler: webhooks.NewHandler(webhooks.Config{
			Store:       st,
			Deliveries:  whStore,
			PulseSecret: "pulse-secret",
		}),
	})

	// Missing webhook auth must fail with 401 without requiring MILL_API_TOKEN.
	rec := doReq(t, h, http.MethodPost, "/webhooks/pulse", "", map[string]any{
		"status": "firing", "alert": "High memory on ct122", "severity": "warning",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth webhook got %d, want 401", rec.Code)
	}

	// Valid webhook Bearer secret creates a spec slice; replay deduplicates.
	req := httptest.NewRequest(http.MethodPost, "/webhooks/pulse", strings.NewReader(`{"status":"firing","alert":"High memory on ct122","severity":"warning"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer pulse-secret")
	req.Header.Set("X-Pulse-Delivery", "deliv-mounted-1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"created"`) {
		t.Fatalf("valid pulse webhook got %d %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/webhooks/pulse", strings.NewReader(`{"status":"firing","alert":"High memory on ct122","severity":"warning"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer pulse-secret")
	req2.Header.Set("X-Pulse-Delivery", "deliv-mounted-1")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"duplicate"`) {
		t.Fatalf("duplicate pulse webhook got %d %s", rec2.Code, rec2.Body.String())
	}
}
