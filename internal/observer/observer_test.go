package observer_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/observer"
)

func TestLoadConfigFromEnv(t *testing.T) {
	t.Run("missing required env vars", func(t *testing.T) {
		t.Setenv("MILL_LLM_BASE_URL", "")
		t.Setenv("MILL_LLM_API_KEY", "")
		t.Setenv("MILL_LLM_MODEL", "")

		if _, err := observer.LoadConfigFromEnv(); err == nil {
			t.Fatal("expected error when env vars are missing")
		}
	})

	t.Run("missing api key", func(t *testing.T) {
		t.Setenv("MILL_LLM_BASE_URL", "https://api.example.com/v1")
		t.Setenv("MILL_LLM_API_KEY", "")
		t.Setenv("MILL_LLM_MODEL", "gpt-test")

		if _, err := observer.LoadConfigFromEnv(); err == nil {
			t.Fatal("expected error when MILL_LLM_API_KEY is missing")
		}
	})

	t.Run("missing model", func(t *testing.T) {
		t.Setenv("MILL_LLM_BASE_URL", "https://api.example.com/v1")
		t.Setenv("MILL_LLM_API_KEY", "test-key")
		t.Setenv("MILL_LLM_MODEL", "")

		if _, err := observer.LoadConfigFromEnv(); err == nil {
			t.Fatal("expected error when MILL_LLM_MODEL is missing")
		}
	})

	t.Run("invalid base url scheme", func(t *testing.T) {
		t.Setenv("MILL_LLM_BASE_URL", "ftp://api.example.com/v1")
		t.Setenv("MILL_LLM_API_KEY", "test-key")
		t.Setenv("MILL_LLM_MODEL", "gpt-test")

		if _, err := observer.LoadConfigFromEnv(); err == nil {
			t.Fatal("expected error for non-http(s) base url")
		}
	})

	t.Run("valid env vars", func(t *testing.T) {
		t.Setenv("MILL_LLM_BASE_URL", "https://api.example.com/v1/")
		t.Setenv("MILL_LLM_API_KEY", "test-key")
		t.Setenv("MILL_LLM_MODEL", "gpt-test")

		cfg, err := observer.LoadConfigFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.BaseURL != "https://api.example.com/v1" {
			t.Fatalf("expected trimmed BaseURL, got %q", cfg.BaseURL)
		}
		if cfg.APIKey != "test-key" || cfg.Model != "gpt-test" {
			t.Fatalf("unexpected config values: %+v", cfg)
		}
	})
}

func TestAnalyze_RunLedger(t *testing.T) {
	t.Run("empty events", func(t *testing.T) {
		rep := observer.Analyze(nil)
		if rep.HasEvidence() {
			t.Fatal("expected HasEvidence() to be false for empty events")
		}
		if rep.TotalEvents != 0 || len(rep.Hotspots) != 0 {
			t.Fatalf("expected zero report, got %+v", rep)
		}
	})

	t.Run("aggregates red runs amendments and footprint findings", func(t *testing.T) {
		now := time.Now().UTC()
		events := []domain.RunEvent{
			{
				ID:                1,
				SliceID:           "slice-001",
				Harness:           "claude",
				Model:             "sonnet",
				Phase:             "test",
				Verdict:           domain.VerdictRed,
				Amendments:        3,
				FootprintFindings: 2,
				ReqDiffDirection:  "narrowed",
				Notes:             "sqlite busy error",
				CreatedAt:         now,
			},
			{
				ID:                2,
				SliceID:           "slice-001",
				Harness:           "claude",
				Model:             "sonnet",
				Phase:             "test",
				Verdict:           domain.VerdictRed,
				Amendments:        2,
				FootprintFindings: 1,
				ReqDiffDirection:  "narrowed",
				Notes:             "sqlite busy error",
				CreatedAt:         now.Add(time.Minute),
			},
			{
				ID:        3,
				SliceID:   "slice-002",
				Harness:   "codex",
				Phase:     "lint",
				Verdict:   domain.VerdictGreen,
				CreatedAt: now.Add(2 * time.Minute),
			},
		}
		slices := []domain.Slice{
			{ID: "slice-001", Title: "Fix SQLite locking", State: domain.StateRed},
			{ID: "slice-002", Title: "Add lint check", State: domain.StateGreen},
		}

		rep := observer.AnalyzeWithSlices(events, slices)
		if !rep.HasEvidence() {
			t.Fatal("expected HasEvidence() to be true")
		}
		if rep.TotalEvents != 3 || rep.RedEvents != 2 {
			t.Fatalf("unexpected event counts: total=%d red=%d", rep.TotalEvents, rep.RedEvents)
		}
		if rep.TotalAmendments != 5 || rep.TotalFootprintFindings != 3 {
			t.Fatalf("unexpected totals: amendments=%d findings=%d", rep.TotalAmendments, rep.TotalFootprintFindings)
		}
		if len(rep.SliceIDs) != 2 || rep.SliceIDs[0] != "slice-001" || rep.SliceIDs[1] != "slice-002" {
			t.Fatalf("unexpected SliceIDs: %v", rep.SliceIDs)
		}
		if len(rep.Hotspots) == 0 || rep.Hotspots[0].SliceID != "slice-001" {
			t.Fatalf("expected slice-001 as top hotspot, got %+v", rep.Hotspots)
		}
		top := rep.Hotspots[0]
		if top.Title != "Fix SQLite locking" || top.RedRuns != 2 || top.Amendments != 5 || top.FootprintFindings != 3 {
			t.Fatalf("unexpected top hotspot metrics: %+v", top)
		}
		if len(rep.Patterns) == 0 {
			t.Fatal("expected failure patterns to be detected")
		}
	})
}

func mockChatServer(t *testing.T, status int, content string, checkReq func(*http.Request, []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if checkReq != nil {
			checkReq(r, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = w.Write([]byte(`{"error":{"message":"upstream error"}}`))
			return
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": content,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func sampleReport() observer.Report {
	return observer.Analyze([]domain.RunEvent{
		{
			ID:                10,
			SliceID:           "slice-042",
			Harness:           "claude",
			Phase:             "test",
			Verdict:           domain.VerdictRed,
			Amendments:        4,
			FootprintFindings: 1,
			Notes:             "timeout in store test",
		},
	})
}

func TestPropose_GroundedSuccess(t *testing.T) {
	proposalJSON := `{
		"title": "Increase SQLite busy timeout for slice-042",
		"summary": "Addresses recurring RED test runs in slice-042 by adjusting retry handling.",
		"cited_slice_ids": ["slice-042"],
		"target_files": ["internal/store/sqlite.go"],
		"diff": "--- a/internal/store/sqlite.go\n+++ b/internal/store/sqlite.go\n@@ -1,3 +1,3 @@\n-old\n+new\n"
	}`

	srv := mockChatServer(t, http.StatusOK, "```json\n"+proposalJSON+"\n```", func(r *http.Request, body []byte) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("unexpected auth header: %q", got)
		}
		if !strings.Contains(string(body), "slice-042") {
			t.Errorf("expected request payload to include slice-042")
		}
	})
	defer srv.Close()

	client, err := observer.NewClient(observer.Config{
		BaseURL: srv.URL,
		APIKey:  "secret-token",
		Model:   "test-model",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	prop, err := client.Propose(context.Background(), sampleReport())
	if err != nil {
		t.Fatalf("Propose failed: %v", err)
	}
	if len(prop.CitedSliceIDs) != 1 || prop.CitedSliceIDs[0] != "slice-042" {
		t.Fatalf("unexpected cited_slice_ids: %v", prop.CitedSliceIDs)
	}
	if len(prop.TargetFiles) != 1 || prop.TargetFiles[0] != "internal/store/sqlite.go" {
		t.Fatalf("unexpected target_files: %v", prop.TargetFiles)
	}
}

func TestPropose_RejectsUngrounded(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name: "no cited slice_ids",
			payload: `{
				"title": "Fix issue",
				"summary": "Generic fix without citation.",
				"cited_slice_ids": [],
				"target_files": ["internal/store/sqlite.go"],
				"diff": "--- a/internal/store/sqlite.go\n+++ b/internal/store/sqlite.go\n@@ -1 +1 @@\n-a\n+b\n"
			}`,
		},
		{
			name: "hallucinated slice_id not in report",
			payload: `{
				"title": "Fix slice-999",
				"summary": "Cites slice-999 which is not in evidence.",
				"cited_slice_ids": ["slice-999"],
				"target_files": ["internal/store/sqlite.go"],
				"diff": "--- a/internal/store/sqlite.go\n+++ b/internal/store/sqlite.go\n@@ -1 +1 @@\n-a\n+b\n"
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := mockChatServer(t, http.StatusOK, tc.payload, nil)
			defer srv.Close()

			client, err := observer.NewClient(observer.Config{
				BaseURL: srv.URL,
				APIKey:  "secret-token",
				Model:   "test-model",
			})
			if err != nil {
				t.Fatalf("NewClient failed: %v", err)
			}

			if _, err := client.Propose(context.Background(), sampleReport()); err == nil {
				t.Fatal("expected error for ungrounded proposal")
			}
		})
	}
}

func TestPropose_RejectsProtectedPaths(t *testing.T) {
	cases := []struct {
		name        string
		targetFiles []string
		diff        string
	}{
		{
			name:        "diff touches claims/REQ-0001.yml",
			targetFiles: []string{"claims/REQ-0001.yml"},
			diff:        "--- a/claims/REQ-0001.yml\n+++ b/claims/REQ-0001.yml\n@@ -1 +1 @@\n-a\n+b\n",
		},
		{
			name:        "diff touches ./claims/REQ-0009.yml",
			targetFiles: []string{"internal/store/sqlite.go"},
			diff:        "--- a/./claims/REQ-0009.yml\n+++ b/./claims/REQ-0009.yml\n@@ -1 +1 @@\n-a\n+b\n",
		},
		{
			name:        "diff touches .intent/lock.json",
			targetFiles: []string{".intent/lock.json"},
			diff:        "diff --git a/.intent/lock.json b/.intent/lock.json\n--- a/.intent/lock.json\n+++ b/.intent/lock.json\n@@ -1 +1 @@\n-a\n+b\n",
		},
		{
			name:        "diff touches .intentgate.yml",
			targetFiles: []string{".intentgate.yml"},
			diff:        "--- a/.intentgate.yml\n+++ b/.intentgate.yml\n@@ -1 +1 @@\n-a\n+b\n",
		},
		{
			name:        "target_files lists claims even if diff does not",
			targetFiles: []string{"claims/REQ-0002.yml"},
			diff:        "--- a/internal/store/sqlite.go\n+++ b/internal/store/sqlite.go\n@@ -1 +1 @@\n-a\n+b\n",
		},
		{
			name:        "diff --git source path targets claims",
			targetFiles: []string{"README.md"},
			diff:        "diff --git a/claims/REQ-0001.yml b/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-a\n+b\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prop := map[string]any{
				"title":           "Update protected file for slice-042",
				"summary":         "Cites slice-042.",
				"cited_slice_ids": []string{"slice-042"},
				"target_files":    tc.targetFiles,
				"diff":            tc.diff,
			}
			raw, _ := json.Marshal(prop)
			srv := mockChatServer(t, http.StatusOK, string(raw), nil)
			defer srv.Close()

			client, err := observer.NewClient(observer.Config{
				BaseURL: srv.URL,
				APIKey:  "secret-token",
				Model:   "test-model",
			})
			if err != nil {
				t.Fatalf("NewClient failed: %v", err)
			}

			if _, err := client.Propose(context.Background(), sampleReport()); err == nil {
				t.Fatalf("expected protected path rejection for %s", tc.name)
			}
		})
	}
}

func TestPropose_ErrorHandling(t *testing.T) {
	t.Run("empty report rejected without network call", func(t *testing.T) {
		client, err := observer.NewClient(observer.Config{
			BaseURL: "https://api.example.com/v1",
			APIKey:  "secret-token",
			Model:   "test-model",
		})
		if err != nil {
			t.Fatalf("NewClient failed: %v", err)
		}
		if _, err := client.Propose(context.Background(), observer.Report{}); err == nil {
			t.Fatal("expected error for empty report")
		}
	})

	t.Run("upstream http error", func(t *testing.T) {
		srv := mockChatServer(t, http.StatusBadGateway, "", nil)
		defer srv.Close()

		client, err := observer.NewClient(observer.Config{
			BaseURL: srv.URL,
			APIKey:  "secret-token",
			Model:   "test-model",
		})
		if err != nil {
			t.Fatalf("NewClient failed: %v", err)
		}
		if _, err := client.Propose(context.Background(), sampleReport()); err == nil {
			t.Fatal("expected error on 502 response")
		}
	})

	t.Run("invalid json content from llm", func(t *testing.T) {
		srv := mockChatServer(t, http.StatusOK, "not-json", nil)
		defer srv.Close()

		client, err := observer.NewClient(observer.Config{
			BaseURL: srv.URL,
			APIKey:  "secret-token",
			Model:   "test-model",
		})
		if err != nil {
			t.Fatalf("NewClient failed: %v", err)
		}
		if _, err := client.Propose(context.Background(), sampleReport()); err == nil {
			t.Fatal("expected error on invalid proposal JSON")
		}
	})
}
