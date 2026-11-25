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
		{"/", http.StatusOK, "text/html", []string{"<title>mill</title>"}},
		{"/static/app.css", http.StatusOK, "text/css", []string{"--accent:", ".risk-high", ".slice-card-high-risk"}},
		{"/static/app.js", http.StatusOK, "javascript", []string{"/auth/login", "riskBadge", "riskRank"}},
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
