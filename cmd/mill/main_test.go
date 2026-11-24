package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/autox-my/mill/internal/domain"
)

func runCLI(t *testing.T, dbPath string, args ...string) (int, string, string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	fullArgs := append([]string{"--db", dbPath}, args...)
	code := run(fullArgs, &outBuf, &errBuf)
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

	code, out, errOut = runCLI(t, dbPath, "slice", "list", "--state", "green", "--json")
	if code != 0 {
		t.Fatalf("slice list failed (%d): %s %s", code, out, errOut)
	}
	var slices []domain.Slice
	if err := json.Unmarshal([]byte(out), &slices); err != nil || len(slices) != 1 || slices[0].ID != "S-10" {
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
}
