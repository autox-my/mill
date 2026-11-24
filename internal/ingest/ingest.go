// Package ingest parses governance and gate tool outputs into validated domain.RunEvent records.
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/autox-my/mill/internal/domain"
)

// Meta supplies run context that is not present in tool stdout/JSON outputs.
type Meta struct {
	SliceID     string
	Harness     string
	Model       string
	Phase       string
	DurationSec int
}

// Parse dispatches raw tool output to the parser matching source.
func Parse(source string, meta Meta, data []byte) (domain.RunEvent, error) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "intentgate", "intentgate-verify", "intentgate_verify":
		return ParseIntentGateJSON(meta, data)
	case "req", "req-diff", "req_diff":
		return ParseReqDiff(meta, data)
	case "check", "check.sh", "check-sh", "check_sh":
		return ParseCheckOutput(meta, data)
	default:
		return domain.RunEvent{}, fmt.Errorf("%w: unknown ingest source %q", domain.ErrInvalidInput, source)
	}
}

type intentGateReport struct {
	Schema            string            `json:"schema"`
	Verdict           string            `json:"verdict"`
	Outcome           string            `json:"outcome"`
	ExitCode          int               `json:"exit_code"`
	Summary           string            `json:"summary"`
	Notes             string            `json:"notes"`
	Problems          []string          `json:"problems"`
	Findings          []json.RawMessage `json:"findings"`
	FootprintFindings int               `json:"footprint_findings"`
	Amendments        json.RawMessage   `json:"amendments"`
}

// ParseIntentGateJSON converts `intentgate verify --format json` output into a validated RunEvent.
func ParseIntentGateJSON(meta Meta, data []byte) (domain.RunEvent, error) {
	payload, err := extractJSONObject(data)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("%w: intentgate json: %v", domain.ErrInvalidInput, err)
	}

	var report intentGateReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return domain.RunEvent{}, fmt.Errorf("%w: invalid intentgate json: %v", domain.ErrInvalidInput, err)
	}

	findings := len(report.Findings)
	if findings == 0 && report.FootprintFindings > 0 {
		findings = report.FootprintFindings
	}
	amendments, err := parseCountOrArray(report.Amendments)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("%w: invalid amendments field: %v", domain.ErrInvalidInput, err)
	}
	verdict, err := resolveIntentGateVerdict(report, findings)
	if err != nil {
		return domain.RunEvent{}, err
	}

	ev := newRunEvent(meta, verdict)
	ev.FootprintFindings = findings
	ev.Amendments = amendments
	ev.Notes = formatIntentGateNotes(report, findings, amendments)
	return validateEvent(ev)
}

func resolveIntentGateVerdict(report intentGateReport, findings int) (domain.Verdict, error) {
	if v := strings.ToUpper(strings.TrimSpace(report.Verdict)); v != "" {
		if domain.ValidVerdict(domain.Verdict(v)) {
			return domain.Verdict(v), nil
		}
		return "", fmt.Errorf("%w: unknown intentgate verdict %q", domain.ErrInvalidInput, report.Verdict)
	}

	switch strings.ToLower(strings.TrimSpace(report.Outcome)) {
	case "green", "pass", "passed", "ok", "no-lock":
		if findings > 0 || report.ExitCode != 0 {
			return domain.VerdictRed, nil
		}
		return domain.VerdictGreen, nil
	case "red", "fail", "failed", "violation", "blocked":
		return domain.VerdictRed, nil
	case "undecidable", "unverifiable", "unknown", "error":
		return domain.VerdictUndecidable, nil
	case "":
		return "", fmt.Errorf("%w: intentgate output missing verdict and outcome", domain.ErrInvalidInput)
	default:
		return "", fmt.Errorf("%w: unknown intentgate outcome %q", domain.ErrInvalidInput, report.Outcome)
	}
}

func formatIntentGateNotes(report intentGateReport, findings, amendments int) string {
	var parts []string
	if report.Outcome != "" {
		parts = append(parts, "outcome="+report.Outcome)
	}
	parts = append(parts, fmt.Sprintf("findings=%d", findings), fmt.Sprintf("amendments=%d", amendments))
	if msg := strings.TrimSpace(report.Summary); msg != "" {
		parts = append(parts, msg)
	} else if msg := strings.TrimSpace(report.Notes); msg != "" {
		parts = append(parts, msg)
	}
	if len(report.Problems) > 0 {
		parts = append(parts, "problems: "+strings.Join(report.Problems, ", "))
	}
	return strings.Join(parts, "; ")
}

type reqDiffJSON struct {
	Schema         string         `json:"schema"`
	Counts         map[string]int `json:"counts"`
	Triggered      []string       `json:"triggered"`
	Contradictions struct {
		Added []json.RawMessage `json:"added"`
	} `json:"contradictions"`
}

// ParseReqDiff converts `req diff` text or JSON output into a validated RunEvent.
func ParseReqDiff(meta Meta, data []byte) (domain.RunEvent, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return domain.RunEvent{}, fmt.Errorf("%w: empty req diff output", domain.ErrInvalidInput)
	}

	counts, triggered, contradictionsAdded, err := parseReqDiffBody(trimmed)
	if err != nil {
		return domain.RunEvent{}, err
	}

	direction, verdict := classifyReqDiff(counts, triggered, contradictionsAdded)
	ev := newRunEvent(meta, verdict)
	ev.ReqDiffDirection = direction
	ev.Notes = formatReqDiffNotes(direction, counts, triggered)
	return validateEvent(ev)
}

func parseReqDiffBody(trimmed []byte) (map[string]int, []string, int, error) {
	if jsonPayload, err := extractJSONObject(trimmed); err == nil {
		var doc reqDiffJSON
		if err := json.Unmarshal(jsonPayload, &doc); err != nil {
			return nil, nil, 0, fmt.Errorf("%w: invalid req diff json: %v", domain.ErrInvalidInput, err)
		}
		if len(doc.Counts) == 0 {
			return nil, nil, 0, fmt.Errorf("%w: req diff json missing counts", domain.ErrInvalidInput)
		}
		return doc.Counts, doc.Triggered, len(doc.Contradictions.Added), nil
	}

	counts, ok := parseReqDiffTextCounts(string(trimmed))
	if !ok {
		return nil, nil, 0, fmt.Errorf("%w: req diff output missing counts", domain.ErrInvalidInput)
	}
	return counts, nil, 0, nil
}

func parseReqDiffTextCounts(text string) (map[string]int, bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "counts:") {
			continue
		}
		counts := make(map[string]int)
		rest := strings.TrimPrefix(line, "counts:")
		for _, item := range strings.Split(rest, ",") {
			fields := strings.Fields(item)
			if len(fields) != 2 {
				continue
			}
			n, err := strconv.Atoi(fields[1])
			if err == nil {
				counts[fields[0]] = n
			}
		}
		if len(counts) > 0 {
			return counts, true
		}
	}
	return nil, false
}

func classifyReqDiff(counts map[string]int, triggered []string, contradictionsAdded int) (string, domain.Verdict) {
	if counts["removed"] > 0 || counts["reshaped"] > 0 || contradictionsAdded > 0 {
		return "breaking", domain.VerdictRed
	}
	if counts["weakened"] > 0 {
		return "loosening", domain.VerdictRed
	}
	if len(triggered) > 0 {
		return "breaking", domain.VerdictRed
	}
	if counts["not-judged"] > 0 {
		return "none", domain.VerdictUndecidable
	}
	if counts["tightened"] > 0 {
		return "tightening", domain.VerdictGreen
	}
	if isUnchangedReqDiff(counts) {
		return "unchanged", domain.VerdictGreen
	}
	return "none", domain.VerdictGreen
}

func isUnchangedReqDiff(counts map[string]int) bool {
	if counts["added"] > 0 || counts["reworded"] > 0 || counts["metadata"] > 0 {
		return false
	}
	return counts["unchanged"] > 0
}

func formatReqDiffNotes(direction string, counts map[string]int, triggered []string) string {
	parts := []string{"direction=" + direction}
	keys := []string{"added", "removed", "weakened", "tightened", "reshaped", "not-judged", "reworded", "metadata"}
	for _, k := range keys {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
		}
	}
	if len(triggered) > 0 {
		parts = append(parts, "triggered="+strings.Join(triggered, ","))
	}
	return strings.Join(parts, "; ")
}

// ParseCheckOutput converts `scripts/check.sh` stdout into a validated RunEvent.
func ParseCheckOutput(meta Meta, data []byte) (domain.RunEvent, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return domain.RunEvent{}, fmt.Errorf("%w: empty check output", domain.ErrInvalidInput)
	}

	var passed int
	var failed, skipped []string
	var verdict domain.Verdict

	for _, rawLine := range strings.Split(trimmed, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "PASS "):
			passed++
		case strings.HasPrefix(line, "FAIL "):
			failed = append(failed, strings.TrimSpace(strings.TrimPrefix(line, "FAIL ")))
		case strings.HasPrefix(line, "SKIP "):
			skipped = append(skipped, parseSkippedStep(strings.TrimPrefix(line, "SKIP ")))
		case strings.HasPrefix(line, "verdict:"):
			v := domain.Verdict(strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(line, "verdict:"))))
			if !domain.ValidVerdict(v) {
				return domain.RunEvent{}, fmt.Errorf("%w: invalid check verdict %q", domain.ErrInvalidInput, v)
			}
			verdict = v
		}
	}

	if verdict == "" {
		return domain.RunEvent{}, fmt.Errorf("%w: check output missing verdict line", domain.ErrInvalidInput)
	}

	ev := newRunEvent(meta, verdict)
	ev.Notes = formatCheckNotes(passed, failed, skipped)
	return validateEvent(ev)
}

func parseSkippedStep(rest string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "unknown"
	}
	return fields[0]
}

func formatCheckNotes(passed int, failed, skipped []string) string {
	var parts []string
	if len(failed) > 0 {
		parts = append(parts, "failed: "+strings.Join(failed, ", "))
	}
	if len(skipped) > 0 {
		parts = append(parts, "skipped: "+strings.Join(skipped, ", "))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("all steps passed (%d passed)", passed)
	}
	parts = append(parts, fmt.Sprintf("%d passed", passed))
	return strings.Join(parts, "; ")
}

func newRunEvent(meta Meta, verdict domain.Verdict) domain.RunEvent {
	return domain.RunEvent{
		SliceID:     meta.SliceID,
		Harness:     meta.Harness,
		Model:       meta.Model,
		Phase:       meta.Phase,
		Verdict:     verdict,
		DurationSec: meta.DurationSec,
	}
}

func validateEvent(ev domain.RunEvent) (domain.RunEvent, error) {
	if err := domain.ValidateRunEvent(ev); err != nil {
		return domain.RunEvent{}, err
	}
	return ev, nil
}

func extractJSONObject(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty input")
	}
	if trimmed[0] == '{' {
		return trimmed, nil
	}
	if idx := bytes.Index(trimmed, []byte("\n{")); idx >= 0 {
		return bytes.TrimSpace(trimmed[idx+1:]), nil
	}
	return nil, fmt.Errorf("no JSON object found")
}

func parseCountOrArray(raw json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, nil
	}
	if trimmed[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return 0, err
		}
		return len(items), nil
	}
	var n int
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return 0, err
	}
	return n, nil
}
