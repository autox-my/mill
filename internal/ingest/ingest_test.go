package ingest

import (
	"errors"
	"strings"
	"testing"

	"github.com/autox-my/mill/internal/domain"
)

func validMeta() Meta {
	return Meta{
		SliceID:     "S-08",
		Harness:     "claude-code",
		Model:       "claude-sonnet-4",
		Phase:       "verify",
		DurationSec: 12,
	}
}

func TestParseIntentGateJSON_GreenNoLock(t *testing.T) {
	raw := []byte(`{
		"schema": "intentgate.verify/1",
		"outcome": "no-lock",
		"exit_code": 0,
		"problems": [],
		"locks": [],
		"findings": []
	}`)

	ev, err := ParseIntentGateJSON(validMeta(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Verdict != domain.VerdictGreen {
		t.Fatalf("expected GREEN, got %q", ev.Verdict)
	}
	if ev.FootprintFindings != 0 || ev.Amendments != 0 {
		t.Fatalf("expected 0 findings and 0 amendments, got %d and %d", ev.FootprintFindings, ev.Amendments)
	}
	if ev.SliceID != "S-08" || ev.Harness != "claude-code" || ev.Model != "claude-sonnet-4" || ev.Phase != "verify" || ev.DurationSec != 12 {
		t.Fatalf("meta fields not propagated: %+v", ev)
	}
	if !strings.Contains(ev.Notes, "no-lock") {
		t.Fatalf("expected outcome in notes, got %q", ev.Notes)
	}
}

func TestParseIntentGateJSON_RealOutcomes(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantVerdict  domain.Verdict
		wantFindings int
	}{
		{
			name:        "clean",
			raw:         `{"outcome":"clean","exit_code":0,"problems":[],"locks":[],"findings":[]}`,
			wantVerdict: domain.VerdictGreen,
		},
		{
			name:        "skipped",
			raw:         `{"outcome":"skipped","exit_code":0,"problems":[],"locks":[],"findings":[]}`,
			wantVerdict: domain.VerdictGreen,
		},
		{
			name:         "findings",
			raw:          `{"outcome":"findings","exit_code":1,"problems":[],"locks":[],"findings":[{"path":"README.md"}]}`,
			wantVerdict:  domain.VerdictRed,
			wantFindings: 1,
		},
		{
			name:        "unverifiable",
			raw:         `{"outcome":"unverifiable","exit_code":3,"problems":["bad config"],"locks":[],"findings":[]}`,
			wantVerdict: domain.VerdictUndecidable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseIntentGateJSON(validMeta(), []byte(tc.raw))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ev.Verdict != tc.wantVerdict {
				t.Fatalf("got verdict %q, want %q", ev.Verdict, tc.wantVerdict)
			}
			if ev.FootprintFindings != tc.wantFindings {
				t.Fatalf("got findings %d, want %d", ev.FootprintFindings, tc.wantFindings)
			}
		})
	}
}

func TestParseIntentGateJSON_RedWithFindingsAndAmendments(t *testing.T) {
	raw := []byte(`{
		"schema": "intentgate.verify/1",
		"outcome": "fail",
		"exit_code": 1,
		"summary": "footprint violation in cmd/mill/main.go",
		"amendments": 2,
		"problems": ["out-of-footprint edit"],
		"findings": [
			{"file": "cmd/mill/main.go", "message": "not in footprint"},
			{"file": "internal/store/sqlite.go", "message": "not in footprint"}
		]
	}`)

	ev, err := ParseIntentGateJSON(validMeta(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Verdict != domain.VerdictRed {
		t.Fatalf("expected RED, got %q", ev.Verdict)
	}
	if ev.FootprintFindings != 2 {
		t.Fatalf("expected 2 findings, got %d", ev.FootprintFindings)
	}
	if ev.Amendments != 2 {
		t.Fatalf("expected 2 amendments, got %d", ev.Amendments)
	}
	if !strings.Contains(ev.Notes, "footprint violation") {
		t.Fatalf("expected summary in notes, got %q", ev.Notes)
	}
}

func TestParseIntentGateJSON_UndecidableAndArrayAmendments(t *testing.T) {
	raw := []byte(`{
		"schema": "intentgate.verify/1",
		"outcome": "undecidable",
		"exit_code": 2,
		"amendments": [{"id": "A-1"}],
		"problems": ["missing base commit"],
		"findings": []
	}`)

	ev, err := ParseIntentGateJSON(validMeta(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Verdict != domain.VerdictUndecidable {
		t.Fatalf("expected UNDECIDABLE, got %q", ev.Verdict)
	}
	if ev.Amendments != 1 {
		t.Fatalf("expected 1 amendment from array, got %d", ev.Amendments)
	}
	if !strings.Contains(ev.Notes, "missing base commit") {
		t.Fatalf("expected problem in notes, got %q", ev.Notes)
	}
}

func TestParseIntentGateJSON_ExplicitVerdictAndLeadingWarning(t *testing.T) {
	raw := []byte("intentgate: warning: cannot append to the audit log\n" + `{
		"schema": "intentgate.verify/1",
		"verdict": "GREEN",
		"outcome": "pass",
		"exit_code": 0,
		"findings": []
	}`)

	ev, err := ParseIntentGateJSON(validMeta(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Verdict != domain.VerdictGreen {
		t.Fatalf("expected GREEN, got %q", ev.Verdict)
	}
}

func TestParseIntentGateJSON_MalformedAndEmpty(t *testing.T) {
	cases := []struct {
		name string
		meta Meta
		data []byte
	}{
		{"empty", validMeta(), nil},
		{"whitespace", validMeta(), []byte("   \n\t ")},
		{"bad json", validMeta(), []byte("{not-json")},
		{"missing outcome", validMeta(), []byte(`{"schema":"intentgate.verify/1"}`)},
		{"invalid meta", Meta{Harness: "h", Phase: "p"}, []byte(`{"outcome":"pass","exit_code":0}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseIntentGateJSON(tc.meta, tc.data)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput for %s, got %v", tc.name, err)
			}
		})
	}
}

func TestParseReqDiff_TextDirections(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantDir   string
		wantVerd  domain.Verdict
		wantNotes string
	}{
		{
			name: "unchanged",
			input: `corpus root: /repo
# req diff: HEAD~1 (abc) -> working-tree
registry: unchanged
contradictions: none added or resolved across 1 selection(s)
counts: added 0, removed 0, weakened 0, tightened 0, reshaped 0, not-judged 0, reworded 0, metadata 0, unchanged 5`,
			wantDir:   "unchanged",
			wantVerd:  domain.VerdictGreen,
			wantNotes: "unchanged",
		},
		{
			name: "tightening",
			input: `# req diff: main -> head
counts: added 1, removed 0, weakened 0, tightened 2, reshaped 0, not-judged 0, reworded 0, metadata 0, unchanged 3`,
			wantDir:   "tightening",
			wantVerd:  domain.VerdictGreen,
			wantNotes: "tightened=2",
		},
		{
			name: "loosening",
			input: `# req diff: main -> head
counts: added 0, removed 0, weakened 1, tightened 0, reshaped 0, not-judged 0, reworded 0, metadata 0, unchanged 4`,
			wantDir:   "loosening",
			wantVerd:  domain.VerdictRed,
			wantNotes: "weakened=1",
		},
		{
			name: "breaking",
			input: `# req diff: main -> head
counts: added 0, removed 1, weakened 0, tightened 0, reshaped 1, not-judged 0, reworded 0, metadata 0, unchanged 3`,
			wantDir:   "breaking",
			wantVerd:  domain.VerdictRed,
			wantNotes: "removed=1",
		},
		{
			name: "not-judged",
			input: `# req diff: main -> head
counts: added 0, removed 0, weakened 0, tightened 0, reshaped 0, not-judged 2, reworded 0, metadata 0, unchanged 3`,
			wantDir:   "none",
			wantVerd:  domain.VerdictUndecidable,
			wantNotes: "not-judged=2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseReqDiff(validMeta(), []byte(tc.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ev.ReqDiffDirection != tc.wantDir {
				t.Fatalf("expected direction %q, got %q", tc.wantDir, ev.ReqDiffDirection)
			}
			if ev.Verdict != tc.wantVerd {
				t.Fatalf("expected verdict %q, got %q", tc.wantVerd, ev.Verdict)
			}
			if !strings.Contains(ev.Notes, tc.wantNotes) {
				t.Fatalf("expected notes to contain %q, got %q", tc.wantNotes, ev.Notes)
			}
		})
	}
}

func TestParseReqDiff_JSONFormat(t *testing.T) {
	raw := []byte(`corpus root: /repo
{
  "schema": "req.diff/1",
  "counts": {
    "added": 0,
    "metadata": 0,
    "not-judged": 0,
    "removed": 0,
    "reshaped": 0,
    "reworded": 0,
    "tightened": 1,
    "unchanged": 4,
    "weakened": 0
  },
  "contradictions": {"added": [], "resolved": []},
  "kinds_changed": false,
  "registry_changed": false,
  "triggered": []
}`)

	ev, err := ParseReqDiff(validMeta(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.ReqDiffDirection != "tightening" {
		t.Fatalf("expected tightening, got %q", ev.ReqDiffDirection)
	}
	if ev.Verdict != domain.VerdictGreen {
		t.Fatalf("expected GREEN, got %q", ev.Verdict)
	}
}

func TestParseReqDiff_Malformed(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("   "), []byte("random text without counts")} {
		_, err := ParseReqDiff(validMeta(), raw)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for %q, got %v", string(raw), err)
		}
	}
}

func TestParseCheckOutput_GreenAndRed(t *testing.T) {
	green := []byte(`PASS gofmt
PASS go vet
PASS tests
PASS req canon
PASS req gate
verdict: GREEN
`)
	ev, err := ParseCheckOutput(validMeta(), green)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Verdict != domain.VerdictGreen {
		t.Fatalf("expected GREEN, got %q", ev.Verdict)
	}
	if !strings.Contains(ev.Notes, "5 passed") {
		t.Fatalf("expected passed count in notes, got %q", ev.Notes)
	}

	red := []byte(`PASS gofmt
FAIL go vet
     internal/ingest/ingest.go:10: unreachable code
PASS tests
SKIP req not found (a SKIP fails the gate)
verdict: RED
`)
	ev, err = ParseCheckOutput(validMeta(), red)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Verdict != domain.VerdictRed {
		t.Fatalf("expected RED, got %q", ev.Verdict)
	}
	if !strings.Contains(ev.Notes, "go vet") || !strings.Contains(ev.Notes, "req") {
		t.Fatalf("expected failed and skipped step names in notes, got %q", ev.Notes)
	}
}

func TestParseCheckOutput_Malformed(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("PASS gofmt\nPASS tests\n"),
		[]byte("verdict: UNKNOWN\n"),
	}
	for _, raw := range cases {
		_, err := ParseCheckOutput(validMeta(), raw)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for %q, got %v", string(raw), err)
		}
	}
}

func TestParse_Dispatcher(t *testing.T) {
	meta := validMeta()

	if _, err := Parse("intentgate", meta, []byte(`{"outcome":"no-lock","exit_code":0}`)); err != nil {
		t.Fatalf("intentgate dispatch failed: %v", err)
	}
	if _, err := Parse("req-diff", meta, []byte(`counts: added 0, removed 0, weakened 0, tightened 0, reshaped 0, not-judged 0, reworded 0, metadata 0, unchanged 2`)); err != nil {
		t.Fatalf("req-diff dispatch failed: %v", err)
	}
	if _, err := Parse("check.sh", meta, []byte("PASS gofmt\nverdict: GREEN\n")); err != nil {
		t.Fatalf("check.sh dispatch failed: %v", err)
	}
	if _, err := Parse("unknown-tool", meta, []byte("hi")); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for unknown source, got %v", err)
	}
}
