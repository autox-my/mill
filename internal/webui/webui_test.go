package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Proves REQ-0011: embedded web UI serves static shell assets and rejects missing paths.
func TestWebUI_ServesEmbeddedAssets(t *testing.T) {
	h := Handler()

	cases := []struct {
		path        string
		wantCode    int
		wantType    string
		wantContain []string
	}{
		{"/", http.StatusOK, "text/html", []string{"<title>mill</title>", "kpi-cycle-time", "kpi-spend", "telemetry-tbody"}},
		{"/static/app.css", http.StatusOK, "text/css", []string{"--accent:", ".risk-high", ".slice-card-high-risk"}},
		{"/static/app.js", http.StatusOK, "javascript", []string{"/auth/login", "riskBadge", "riskRank", "formatCost", "renderTelemetry"}},
		{"/static/missing.js", http.StatusNotFound, "", nil},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != tc.wantCode {
			t.Errorf("GET %s status = %d, want %d", tc.path, rec.Code, tc.wantCode)
		}
		if tc.wantType != "" && !strings.Contains(rec.Header().Get("Content-Type"), tc.wantType) {
			t.Errorf("GET %s content-type = %q, want %q", tc.path, rec.Header().Get("Content-Type"), tc.wantType)
		}
		for _, sub := range tc.wantContain {
			if !strings.Contains(rec.Body.String(), sub) {
				t.Errorf("GET %s missing %q", tc.path, sub)
			}
		}
		if tc.wantCode == http.StatusOK && rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s missing nosniff header", tc.path)
		}
	}
}

func TestBoardAppHTML_InlinesAssetsAndBootstrap(t *testing.T) {
	bootstrap := map[string]any{
		"slices": []map[string]any{
			{"id": "S-1", "title": "Close </script> safely\u2028\u2029"},
		},
	}

	html, err := BoardAppHTML(bootstrap)
	if err != nil {
		t.Fatalf("BoardAppHTML returned error: %v", err)
	}

	if strings.Contains(html, `<link rel="stylesheet" href="/static/app.css">`) {
		t.Error("expected stylesheet link to be replaced")
	}
	if strings.Contains(html, `<script type="module" src="/static/app.js"></script>`) {
		t.Error("expected module script src to be replaced")
	}
	if !strings.Contains(html, "<style>") || !strings.Contains(html, "--accent:") {
		t.Error("expected inline app.css inside <style>")
	}
	if !strings.Contains(html, "<script type=\"module\">") || !strings.Contains(html, "window.__MILL_BOOTSTRAP__") {
		t.Error("expected inline app.js and bootstrap script")
	}
	if strings.Contains(html, "Close </script>") {
		t.Error("expected </script> inside bootstrap JSON to be escaped")
	}
	if strings.Contains(html, "\u2028") || strings.Contains(html, "\u2029") {
		t.Error("expected U+2028 and U+2029 to be escaped")
	}

	noBootstrap, err := BoardAppHTML(nil)
	if err != nil {
		t.Fatalf("BoardAppHTML(nil) returned error: %v", err)
	}
	if strings.Contains(noBootstrap, "window.__MILL_BOOTSTRAP__ =") {
		t.Error("expected no bootstrap assignment when bootstrap is nil")
	}
}
