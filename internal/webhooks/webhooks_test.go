package webhooks_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/store"
	"github.com/autox-my/mill/internal/webhooks"
)

func signHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func newTestEnv(t *testing.T) (*store.Store, *webhooks.SQLiteDeliveryStore, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mill.db"))
	if err != nil {
		t.Fatalf("open slice store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ds, err := webhooks.OpenDeliveryStore(filepath.Join(dir, "deliveries.db"))
	if err != nil {
		t.Fatalf("open delivery store: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })

	h := webhooks.NewHandler(webhooks.Config{
		Store:         st,
		Deliveries:    ds,
		ForgejoSecret: "forgejo-secret",
		PulseSecret:   "pulse-secret",
	})
	return st, ds, h
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("MILL_WEBHOOK_FORGEJO_SECRET", "fj-env-secret")
	t.Setenv("MILL_WEBHOOK_PULSE_SECRET", "pl-env-secret")

	cfg := webhooks.LoadConfigFromEnv(nil, nil)
	if cfg.ForgejoSecret != "fj-env-secret" {
		t.Fatalf("expected ForgejoSecret=fj-env-secret, got %q", cfg.ForgejoSecret)
	}
	if cfg.PulseSecret != "pl-env-secret" {
		t.Fatalf("expected PulseSecret=pl-env-secret, got %q", cfg.PulseSecret)
	}
}

func TestRoutingAndFailSafe503(t *testing.T) {
	_, ds, h := newTestEnv(t)

	// Unknown webhook name -> 404
	req := httptest.NewRequest(http.MethodPost, "/webhooks/unknown", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown webhook, got %d", rec.Code)
	}

	// Store nil -> 503
	hNoStore := webhooks.NewHandler(webhooks.Config{
		Deliveries:    ds,
		ForgejoSecret: "s",
	})
	req = httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer s")
	rec = httptest.NewRecorder()
	hNoStore.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when store is nil, got %d", rec.Code)
	}

	// Deliveries nil -> 503
	hNoDeliveries := webhooks.NewHandler(webhooks.Config{
		Store:         &store.Store{},
		ForgejoSecret: "s",
	})
	req = httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer s")
	rec = httptest.NewRecorder()
	hNoDeliveries.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when deliveries is nil, got %d", rec.Code)
	}

	// Empty secret for target -> 503
	hNoSecret := webhooks.NewHandler(webhooks.Config{
		Store:       &store.Store{},
		Deliveries:  ds,
		PulseSecret: "pulse-only",
	})
	req = httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	hNoSecret.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when target secret is empty, got %d", rec.Code)
	}
}

func TestAuthenticationModes(t *testing.T) {
	_, _, h := newTestEnv(t)
	body := []byte(`{"action":"ping"}`)

	cases := []struct {
		name       string
		path       string
		headerName string
		headerVal  string
		wantCode   int
	}{
		{"missing auth", "/webhooks/forgejo", "", "", http.StatusUnauthorized},
		{"wrong bearer", "/webhooks/forgejo", "Authorization", "Bearer wrong", http.StatusUnauthorized},
		{"valid bearer forgejo", "/webhooks/forgejo", "Authorization", "Bearer forgejo-secret", http.StatusOK},
		{"valid X-Forgejo-Signature raw", "/webhooks/forgejo", "X-Forgejo-Signature", signHMAC("forgejo-secret", body), http.StatusOK},
		{"valid X-Hub-Signature-256 prefixed upper", "/webhooks/forgejo", "X-Hub-Signature-256", "sha256=" + strings.ToUpper(signHMAC("forgejo-secret", body)), http.StatusOK},
		{"valid X-Pulse-Signature", "/webhooks/pulse", "X-Pulse-Signature", "sha256=" + signHMAC("pulse-secret", body), http.StatusOK},
		{"valid X-Mill-Signature", "/webhooks/pulse", "X-Mill-Signature", signHMAC("pulse-secret", body), http.StatusOK},
		{"invalid hmac", "/webhooks/pulse", "X-Pulse-Signature", "sha256=deadbeef", http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			req.Header.Set("X-Delivery-ID", "auth-test-"+tc.name)
			if tc.headerName != "" {
				req.Header.Set(tc.headerName, tc.headerVal)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("got status %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestMaxBodyBytesAndInvalidJSON(t *testing.T) {
	_, _, h := newTestEnv(t)

	// > 1 MiB body -> 413
	oversized := bytes.Repeat([]byte("a"), (1<<20)+10)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", bytes.NewReader(oversized))
	req.Header.Set("Authorization", "Bearer forgejo-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d", rec.Code)
	}

	// Invalid JSON -> 400
	req = httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", strings.NewReader(`{not-json`))
	req.Header.Set("Authorization", "Bearer forgejo-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestSanitizeAndEnvelope(t *testing.T) {
	raw := "  hello\x00\x07\n\r\tworld\x7f  " + strings.Repeat("x", 200)
	got := webhooks.SanitizeString(raw, 160)
	if len([]rune(got)) != 160 {
		t.Fatalf("expected length 160, got %d", len([]rune(got)))
	}
	if !strings.HasPrefix(got, "hello world ") {
		t.Fatalf("expected sanitized prefix 'hello world ', got %q", got[:20])
	}
	for _, r := range got {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("found control char %q in sanitized output", r)
		}
	}

	env := webhooks.WrapUntrusted("forgejo", "del-1", "pull_request", []byte(`{"ok":true}`), time.Now().UTC())
	if !env.Untrusted {
		t.Fatalf("expected Untrusted=true")
	}
	if env.Source != "forgejo" || env.DeliveryID != "del-1" || env.EventType != "pull_request" {
		t.Fatalf("unexpected envelope fields: %+v", env)
	}
}

func TestDeliveryStoreAndDeduplication(t *testing.T) {
	st, ds, h := newTestEnv(t)
	ctx := context.Background()

	// Direct store test
	ok, err := ds.RecordDelivery(ctx, webhooks.DeliveryRecord{
		DeliveryID: "d-100",
		Source:     "forgejo",
		EventType:  "push",
		Status:     "ignored",
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil || !ok {
		t.Fatalf("expected first RecordDelivery inserted=true, err=nil; got %v, %v", ok, err)
	}
	ok, err = ds.RecordDelivery(ctx, webhooks.DeliveryRecord{
		DeliveryID: "d-100",
		Source:     "forgejo",
		EventType:  "push",
		Status:     "ignored",
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil || ok {
		t.Fatalf("expected duplicate RecordDelivery inserted=false, err=nil; got %v, %v", ok, err)
	}
	list, err := ds.ListDeliveries(ctx, "forgejo", 10)
	if err != nil || len(list) != 1 || list[0].DeliveryID != "d-100" {
		t.Fatalf("unexpected ListDeliveries: %+v, err=%v", list, err)
	}

	// HTTP deduplication with deterministic fallback delivery_id (no delivery header)
	payload := `{"action":"opened","pull_request":{"title":"New Feature","head":{"ref":"feature/s-77-test"}},"repository":{"full_name":"org/repo"}}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer forgejo-secret")
		req.Header.Set("X-Forgejo-Event", "pull_request")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: expected 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if i == 0 && resp["status"] != "created" {
			t.Fatalf("expected status=created on first delivery, got %v", resp["status"])
		}
		if i == 1 && resp["status"] != "duplicate" {
			t.Fatalf("expected status=duplicate on second delivery, got %v", resp["status"])
		}
	}

	sl, err := st.GetSlice(ctx, "S-77")
	if err != nil {
		t.Fatalf("expected S-77 created, got err: %v", err)
	}
	if sl.State != domain.StateSpec || sl.Title != "New Feature" || sl.Branch != "feature/s-77-test" {
		t.Fatalf("unexpected slice created: %+v", sl)
	}
}

func TestForgejoPullRequestMergedAndIgnored(t *testing.T) {
	st, _, h := newTestEnv(t)
	ctx := context.Background()

	// Seed S-12 in StateInReview so it can transition to StateMerged
	err := st.CreateSlice(ctx, domain.Slice{
		ID:    "S-12",
		Repo:  "org/repo",
		Title: "Existing slice",
		State: domain.StateInReview,
	})
	if err != nil {
		t.Fatalf("seed slice: %v", err)
	}

	mergedPayload := `{"action":"closed","pull_request":{"merged":true,"head":{"ref":"feat/s-12-webhooks"}},"repository":{"full_name":"org/repo"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", strings.NewReader(mergedPayload))
	req.Header.Set("Authorization", "Bearer forgejo-secret")
	req.Header.Set("X-Forgejo-Event", "pull_request")
	req.Header.Set("X-Forgejo-Delivery", "fj-merge-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on merged PR, got %d: %s", rec.Code, rec.Body.String())
	}

	sl, err := st.GetSlice(ctx, "S-12")
	if err != nil {
		t.Fatalf("get S-12: %v", err)
	}
	if sl.State != domain.StateMerged || sl.LastVerdict != domain.VerdictGreen {
		t.Fatalf("expected S-12 merged/GREEN, got %s/%s", sl.State, sl.LastVerdict)
	}

	// Merged PR for non-existent slice -> 200 (no 500)
	missingPayload := `{"action":"closed","pull_request":{"merged":true,"head":{"label":"feat/s-999-missing"}}}`
	req = httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", strings.NewReader(missingPayload))
	req.Header.Set("Authorization", "Bearer forgejo-secret")
	req.Header.Set("X-Forgejo-Event", "pull_request")
	req.Header.Set("X-Forgejo-Delivery", "fj-merge-missing")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for missing slice merge, got %d", rec.Code)
	}
}

func TestPulseAlerts(t *testing.T) {
	st, _, h := newTestEnv(t)
	ctx := context.Background()

	// Firing critical alert
	firing := `{"status":"firing","alert_id":"disk-full-ct103","title":"Disk 95% full\non CT 103","severity":"critical"}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/pulse", strings.NewReader(firing))
	req.Header.Set("Authorization", "Bearer pulse-secret")
	req.Header.Set("X-Pulse-Delivery", "pulse-del-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for firing pulse alert, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sliceID, _ := resp["slice_id"].(string)
	if !strings.HasPrefix(sliceID, "PULSE-") || len(sliceID) != len("PULSE-")+12 {
		t.Fatalf("expected PULSE-<12hex>, got %q", sliceID)
	}
	sl, err := st.GetSlice(ctx, sliceID)
	if err != nil {
		t.Fatalf("get pulse slice: %v", err)
	}
	if sl.Repo != "homelab/pulse" || sl.Risk != domain.RiskHigh || sl.State != domain.StateSpec {
		t.Fatalf("unexpected pulse slice: %+v", sl)
	}
	if sl.Title != "[alert] Disk 95% full on CT 103" {
		t.Fatalf("unexpected pulse title: %q", sl.Title)
	}

	// Duplicate alert_id with new delivery_id -> ignored (ErrAlreadyExists)
	req = httptest.NewRequest(http.MethodPost, "/webhooks/pulse", strings.NewReader(firing))
	req.Header.Set("Authorization", "Bearer pulse-secret")
	req.Header.Set("X-Pulse-Delivery", "pulse-del-2")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for existing pulse slice, got %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "ignored" {
		t.Fatalf("expected status=ignored for existing pulse slice, got %v", resp["status"])
	}

	// Resolved alert -> ignored
	resolved := `{"status":"resolved","alert_id":"cpu-ok","title":"CPU normal"}`
	req = httptest.NewRequest(http.MethodPost, "/webhooks/pulse", strings.NewReader(resolved))
	req.Header.Set("Authorization", "Bearer pulse-secret")
	req.Header.Set("X-Pulse-Delivery", "pulse-del-3")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for resolved alert, got %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "ignored" {
		t.Fatalf("expected status=ignored for resolved alert, got %v", resp["status"])
	}
}
