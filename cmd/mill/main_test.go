package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autox-my/mill/internal/domain"
	"github.com/autox-my/mill/internal/httpapi"
	"github.com/autox-my/mill/internal/observer"
	"github.com/autox-my/mill/internal/store"
)

func runCLI(t *testing.T, dbPath string, args ...string) (int, string, string) {
	t.Helper()
	return runCLIWithStdin(t, dbPath, "", args...)
}

func runCLIWithStdin(t *testing.T, dbPath, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	fullArgs := append([]string{"--db", dbPath}, args...)
	code := runWithIO(fullArgs, strings.NewReader(stdin), &outBuf, &errBuf)
	return code, outBuf.String(), errBuf.String()
}

func TestCLI_EndToEndWorkflow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mill.db")

	code, out, errOut := runCLI(t, dbPath, "init")
	if code != 0 {
		t.Fatalf("init failed (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "create",
		"--id", "S-10",
		"--repo", "autox-my/mill",
		"--title", "CLI end to end",
		"--claims", "REQ-0001,REQ-0002",
		"--branch", "feature/s-10",
	)
	if code != 0 {
		t.Fatalf("slice create failed (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "claim", "--id", "S-10", "--owner", "claude@mac", "--ttl", "30m")
	if code != 0 {
		t.Fatalf("slice claim failed (%d): %s %s", code, out, errOut)
	}

	// Second owner claim while active must exit non-zero.
	code, _, _ = runCLI(t, dbPath, "slice", "claim", "--id", "S-10", "--owner", "opencode@ct120", "--ttl", "30m")
	if code == 0 {
		t.Fatal("expected competing slice claim to fail")
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "renew", "--id", "S-10", "--owner", "claude@mac", "--ttl", "45m")
	if code != 0 {
		t.Fatalf("slice renew failed (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "update", "--id", "S-10", "--state", "red", "--verdict", "RED")
	if code != 0 {
		t.Fatalf("slice update red failed (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "update", "--id", "S-10", "--state", "green", "--verdict", "GREEN")
	if code != 0 {
		t.Fatalf("slice update green failed (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "update", "--id", "S-10", "--risk", "medium")
	if code != 0 || !strings.Contains(out, "risk=medium") {
		t.Fatalf("slice update --risk failed (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "list", "--state", "green")
	if code != 0 || !strings.Contains(out, "risk=medium") {
		t.Fatalf("slice list text missing risk=medium (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "slice", "list", "--state", "green", "--json")
	if code != 0 {
		t.Fatalf("slice list failed (%d): %s %s", code, out, errOut)
	}
	var slices []domain.Slice
	if err := json.Unmarshal([]byte(out), &slices); err != nil || len(slices) != 1 || slices[0].ID != "S-10" || slices[0].Risk != domain.RiskMedium {
		t.Fatalf("unexpected slice list json: err=%v out=%s", err, out)
	}

	code, out, errOut = runCLI(t, dbPath, "run", "record",
		"--slice", "S-10",
		"--harness", "claude-code",
		"--model", "claude-sonnet",
		"--phase", "green",
		"--verdict", "GREEN",
		"--duration", "18",
		"--amendments", "1",
		"--findings", "0",
		"--req-diff", "none",
		"--notes", "ok",
	)
	if code != 0 {
		t.Fatalf("run record failed (%d): %s %s", code, out, errOut)
	}

	// Deterministic floor from amendments=1 prevents lowering risk below medium.
	code, out, errOut = runCLI(t, dbPath, "slice", "update", "--id", "S-10", "--risk", "low")
	if code != 0 || !strings.Contains(out, "risk=medium") {
		t.Fatalf("expected risk floor medium (%d): %s %s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, dbPath, "run", "list", "--slice", "S-10", "--json")
	if code != 0 {
		t.Fatalf("run list failed (%d): %s %s", code, out, errOut)
	}
	var runs []domain.RunEvent
	if err := json.Unmarshal([]byte(out), &runs); err != nil || len(runs) != 1 {
		t.Fatalf("unexpected run list json: err=%v out=%s", err, out)
	}

	code, out, errOut = runCLI(t, dbPath, "stats", "--json")
	if code != 0 {
		t.Fatalf("stats failed (%d): %s %s", code, out, errOut)
	}
	var stats domain.StatsSummary
	if err := json.Unmarshal([]byte(out), &stats); err != nil || stats.TotalSlices != 1 || stats.GreenRuns != 1 {
		t.Fatalf("unexpected stats json: err=%v out=%s", err, out)
	}

	// Text output smoke check.
	code, out, errOut = runCLI(t, dbPath, "stats")
	if code != 0 || !strings.Contains(out, "slices:") {
		t.Fatalf("stats text failed (%d): %s %s", code, out, errOut)
	}

	// Branch-to-slice resolution on slice update (--branch feature/s-10-foo -> S-10).
	code, out, errOut = runCLI(t, dbPath, "slice", "update", "--branch", "feature/s-10-foo", "--state", "merged", "--verdict", "GREEN")
	if code != 0 || !strings.Contains(out, "state=merged") {
		t.Fatalf("slice update --branch failed (%d): %s %s", code, out, errOut)
	}
}

func TestCLI_MCPSubcommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mill.db")
	if code, _, errOut := runCLI(t, dbPath, "init"); code != 0 {
		t.Fatalf("init failed: %s", errOut)
	}
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	code, out, errOut := runCLIWithStdin(t, dbPath, req, "mcp")
	if code != 0 {
		t.Fatalf("mcp failed (%d): %s %s", code, out, errOut)
	}
	if !strings.Contains(out, `"protocolVersion"`) {
		t.Fatalf("expected initialize response, got: %s", out)
	}
}

func TestCLI_ServeFailsSafeWithoutToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mill.db")
	t.Setenv("MILL_API_TOKEN", "")
	code, _, errOut := runCLI(t, dbPath, "serve", "--addr", "127.0.0.1:0")
	if code == 0 {
		t.Fatal("expected serve without MILL_API_TOKEN to fail")
	}
	if !strings.Contains(errOut, "MILL_API_TOKEN") {
		t.Fatalf("expected MILL_API_TOKEN error, got: %s", errOut)
	}
}

func TestCLI_IngestSubcommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mill.db")
	if code, _, errOut := runCLI(t, dbPath, "slice", "create", "--id", "S-20", "--repo", "autox-my/mill", "--title", "Ingest test"); code != 0 {
		t.Fatalf("slice create failed: %s", errOut)
	}

	checkOut := "PASS gofmt\nPASS tests\nverdict: GREEN\n"
	code, out, errOut := runCLIWithStdin(t, dbPath, checkOut,
		"ingest", "--source", "check", "--slice", "S-20", "--harness", "ci", "--phase", "verify",
	)
	if code != 0 {
		t.Fatalf("ingest stdin failed (%d): %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "recorded run #") || !strings.Contains(out, "GREEN") {
		t.Fatalf("unexpected ingest output: %s", out)
	}

	// File + dry-run mode
	f := filepath.Join(t.TempDir(), "check.txt")
	if err := os.WriteFile(f, []byte("FAIL tests\nverdict: RED\n"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	code, out, errOut = runCLI(t, dbPath, "ingest", "--source", "check", "--slice", "S-20", "--harness", "ci", "--phase", "verify", "--file", f, "--dry-run")
	if code != 0 {
		t.Fatalf("ingest dry-run failed (%d): %s %s", code, out, errOut)
	}
	var ev domain.RunEvent
	if err := json.Unmarshal([]byte(out), &ev); err != nil || ev.Verdict != domain.VerdictRed {
		t.Fatalf("unexpected dry-run json: err=%v out=%s", err, out)
	}

	// Branch-to-slice extraction mode (--branch feature/s-20-foo -> S-20)
	code, out, errOut = runCLIWithStdin(t, dbPath, checkOut,
		"ingest", "--source", "check", "--branch", "feature/s-20-ingest-test", "--harness", "github-actions", "--phase", "ci",
	)
	if code != 0 || !strings.Contains(out, "S-20") {
		t.Fatalf("ingest --branch failed (%d): %s %s", code, out, errOut)
	}
}

func TestCLI_ImproveSubcommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mill.db")
	if code, _, errOut := runCLI(t, dbPath, "slice", "create", "--id", "S-30", "--repo", "autox-my/mill", "--title", "Improve test"); code != 0 {
		t.Fatalf("slice create failed: %s", errOut)
	}
	if code, _, errOut := runCLI(t, dbPath, "run", "record", "--slice", "S-30", "--harness", "ci", "--phase", "verify", "--verdict", "RED", "--amendments", "2"); code != 0 {
		t.Fatalf("run record failed: %s", errOut)
	}

	// Report-only mode
	code, out, errOut := runCLI(t, dbPath, "improve", "--report-only")
	if code != 0 {
		t.Fatalf("improve --report-only failed (%d): %s %s", code, out, errOut)
	}
	var rep observer.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.RedEvents != 1 {
		t.Fatalf("unexpected report json: err=%v out=%s", err, out)
	}

	// Full LLM proposal mode with mock OpenAI server
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		propJSON := `{"title":"Fix verify flake","summary":"Addresses S-30 RED run","cited_slice_ids":["S-30"],"target_files":["internal/ingest/ingest.go"],"diff":"--- a/internal/ingest/ingest.go\n+++ b/internal/ingest/ingest.go\n@@ -1 +1 @@\n-old\n+new\n"}`
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": propJSON}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLM.Close()

	t.Setenv("MILL_LLM_BASE_URL", mockLLM.URL)
	t.Setenv("MILL_LLM_API_KEY", "test-key")
	t.Setenv("MILL_LLM_MODEL", "test-model")

	code, out, errOut = runCLI(t, dbPath, "improve")
	if code != 0 {
		t.Fatalf("improve failed (%d): %s %s", code, out, errOut)
	}
	var prop observer.Proposal
	if err := json.Unmarshal([]byte(out), &prop); err != nil || len(prop.CitedSliceIDs) != 1 || prop.CitedSliceIDs[0] != "S-30" {
		t.Fatalf("unexpected proposal json: err=%v out=%s", err, out)
	}
}

func TestCLI_HelpAndErrorPaths(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mill.db")

	if code, out, _ := runCLI(t, dbPath, "--help"); code != 0 || !strings.Contains(out, "improve") {
		t.Fatalf("expected help output, got code=%d out=%s", code, out)
	}
	if code, _, errOut := runCLI(t, dbPath, "unknown-cmd"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("expected unknown command exit 2, got code=%d err=%s", code, errOut)
	}
	if code, _, _ := runCLI(t, dbPath, "ingest", "--source", "bogus", "--slice", "S-1", "--harness", "ci"); code != 1 {
		t.Fatalf("expected bogus ingest source to fail, got code=%d", code)
	}
	t.Setenv("MILL_LLM_BASE_URL", "")
	if code, _, _ := runCLI(t, dbPath, "improve"); code != 1 {
		t.Fatalf("expected improve without LLM config to fail, got code=%d", code)
	}
}

func TestCLI_RemoteHTTPBackend(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "remote.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	srv := httptest.NewServer(httpapi.NewHandler(st, "remote-secret"))
	defer srv.Close()

	t.Setenv("MILL_URL", srv.URL)
	t.Setenv("MILL_API_TOKEN", "remote-secret")

	if code, _, errOut := runCLI(t, "", "slice", "create", "--id", "S-40", "--repo", "autox-my/mill", "--title", "Remote slice"); code != 0 {
		t.Fatalf("remote slice create failed: %s", errOut)
	}
	code, out, errOut := runCLI(t, "", "slice", "list", "--json")
	if code != 0 || !strings.Contains(out, "S-40") {
		t.Fatalf("remote slice list failed (%d): %s %s", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "", "stats", "--json")
	if code != 0 || !strings.Contains(out, `"total_slices": 1`) {
		t.Fatalf("remote stats failed (%d): %s %s", code, out, errOut)
	}

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_slices","arguments":{}}}` + "\n"
	code, out, errOut = runCLIWithStdin(t, "", req, "mcp")
	if code != 0 || !strings.Contains(out, "S-40") {
		t.Fatalf("remote mcp failed (%d): %s %s", code, out, errOut)
	}
}
