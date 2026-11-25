package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestValidStateAndTransition(t *testing.T) {
	if !ValidState(StateSpec) || ValidState("unknown") {
		t.Fatal("ValidState mismatch")
	}
	cases := []struct {
		from SliceState
		to   SliceState
		want bool
	}{
		{StateSpec, StateLocked, true},
		{StateSpec, StateClaimed, true},
		{StateSpec, StateMerged, false},
		{StateClaimed, StateRed, true},
		{StateRed, StateGreen, true},
		{StateGreen, StateInReview, true},
		{StateGreen, StateMerged, true},
		{StateInReview, StateReadyForHuman, true},
		{StateInReview, StateMerged, true},
		{StateReadyForHuman, StateMerged, true},
		{StateMerged, StateClaimed, false},
		{StateSpec, StateSpec, false},
	}
	for _, tc := range cases {
		if got := ValidTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("ValidTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestValidVerdict(t *testing.T) {
	for _, v := range []Verdict{VerdictPending, VerdictGreen, VerdictRed, VerdictUndecidable} {
		if !ValidVerdict(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if ValidVerdict("MAYBE") {
		t.Error("expected MAYBE to be invalid")
	}
}

func TestValidateInputs(t *testing.T) {
	if err := ValidateSliceCreate(Slice{ID: "S-01", Repo: "autox-my/mill", Title: "Init"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateSliceCreate(Slice{Repo: "r", Title: "t"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := ValidateSliceCreate(Slice{ID: "S-01", Title: "t"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := ValidateSliceCreate(Slice{ID: "S-01", Repo: "r"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := ValidateSliceCreate(Slice{ID: "S-01", Repo: "r", Title: "t", State: "bad"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := ValidateSliceCreate(Slice{ID: "S-01", Repo: "r", Title: "t", LastVerdict: "bad"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}

	if err := ValidateClaim("S-01", "claude@mac", time.Minute); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateClaim("", "owner", time.Minute); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := ValidateClaim("S-01", "", time.Minute); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := ValidateClaim("S-01", "owner", 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}

	ev := RunEvent{SliceID: "S-01", Harness: "claude-code", Phase: "green", Verdict: VerdictGreen}
	if err := ValidateRunEvent(ev); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	badEv := ev
	badEv.Amendments = -1
	if err := ValidateRunEvent(badEv); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	badEv = ev
	badEv.Verdict = "NOPE"
	if err := ValidateRunEvent(badEv); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := ValidateSliceCreate(Slice{ID: "S-01", Repo: "r", Title: "t", Risk: "critical"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput for invalid risk, got %v", err)
	}
	if err := ValidateSliceCreate(Slice{ID: "S-01", Repo: "r", Title: "t", Risk: RiskMedium}); err != nil {
		t.Fatalf("unexpected error for valid risk: %v", err)
	}
}

func TestRiskFloorAndOrdering(t *testing.T) {
	for _, r := range []RiskLevel{RiskLow, RiskMedium, RiskHigh} {
		if !ValidRisk(r) {
			t.Errorf("expected ValidRisk(%q) = true", r)
		}
	}
	if ValidRisk("critical") || ValidRisk("") {
		t.Error("expected invalid/empty risk to return false from ValidRisk")
	}

	cases := []struct {
		name string
		ev   RunEvent
		want RiskLevel
	}{
		{"clean green", RunEvent{Verdict: VerdictGreen}, RiskLow},
		{"amendments", RunEvent{Verdict: VerdictGreen, Amendments: 1}, RiskMedium},
		{"red verdict", RunEvent{Verdict: VerdictRed}, RiskMedium},
		{"undecidable verdict", RunEvent{Verdict: VerdictUndecidable}, RiskMedium},
		{"footprint findings", RunEvent{Verdict: VerdictGreen, FootprintFindings: 1}, RiskHigh},
		{"req diff breaking", RunEvent{Verdict: VerdictGreen, ReqDiffDirection: "breaking"}, RiskHigh},
		{"req diff loosening mixed case", RunEvent{Verdict: VerdictGreen, ReqDiffDirection: "Loosening"}, RiskHigh},
		{"findings overrides amendments", RunEvent{Verdict: VerdictRed, Amendments: 2, FootprintFindings: 1}, RiskHigh},
	}
	for _, tc := range cases {
		if got := RunRiskFloor(tc.ev); got != tc.want {
			t.Errorf("%s: RunRiskFloor = %q, want %q", tc.name, got, tc.want)
		}
	}

	runsWithFindings := []RunEvent{
		{Verdict: VerdictGreen},
		{Verdict: VerdictGreen, Amendments: 1},
		{Verdict: VerdictGreen, FootprintFindings: 1},
	}
	if got := ComputeRiskFloor(runsWithFindings); got != RiskHigh {
		t.Fatalf("ComputeRiskFloor = %q, want %q", got, RiskHigh)
	}
	if got := EffectiveRisk(RiskLow, runsWithFindings); got != RiskHigh {
		t.Fatalf("EffectiveRisk(low, findings) = %q, want %q", got, RiskHigh)
	}
	cleanRuns := []RunEvent{{Verdict: VerdictGreen}}
	if got := EffectiveRisk(RiskHigh, cleanRuns); got != RiskHigh {
		t.Fatalf("EffectiveRisk(high, clean) = %q, want %q", got, RiskHigh)
	}
	if got := EffectiveRisk("", nil); got != RiskLow {
		t.Fatalf("EffectiveRisk(empty, nil) = %q, want %q", got, RiskLow)
	}
}

func TestTelemetryAndCycleTime(t *testing.T) {
	base := RunEvent{SliceID: "S-01", Harness: "antigravity", Phase: "verify", Verdict: VerdictGreen, TokensIn: 1200, TokensOut: 300, CostUSD: 0.045}
	if err := ValidateRunEvent(base); err != nil {
		t.Fatalf("valid telemetry rejected: %v", err)
	}
	for _, mutate := range []func(*RunEvent){
		func(e *RunEvent) { e.TokensIn = -1 },
		func(e *RunEvent) { e.TokensOut = -5 },
		func(e *RunEvent) { e.CostUSD = -0.01 },
		func(e *RunEvent) { e.CostUSD = math.NaN() },
		func(e *RunEvent) { e.CostUSD = math.Inf(1) },
	} {
		ev := base
		mutate(&ev)
		if err := ValidateRunEvent(ev); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for invalid telemetry %+v, got %v", ev, err)
		}
	}

	t0 := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
	slices := []Slice{
		{ID: "S-1", State: StateSpec, CreatedAt: t0, UpdatedAt: t0.Add(999 * time.Second)},
		{ID: "S-2", State: StateMerged, CreatedAt: t0, UpdatedAt: t0.Add(100 * time.Second)},
		{ID: "S-3", State: StateMerged, CreatedAt: t0, UpdatedAt: t0.Add(300 * time.Second)},
		{ID: "S-4", State: StateMerged, CreatedAt: t0, UpdatedAt: t0.Add(200 * time.Second)},
	}
	ct := ComputeCycleTime(slices)
	if ct.MergedCount != 3 || ct.AvgSec != 200 || ct.P50Sec != 200 || ct.MaxSec != 300 {
		t.Fatalf("unexpected CycleTimeStats: %+v", ct)
	}
	if empty := ComputeCycleTime(slices[:1]); empty.MergedCount != 0 || empty.AvgSec != 0 {
		t.Fatalf("expected zero CycleTimeStats, got %+v", empty)
	}

	runs := []RunEvent{
		{SliceID: "S-2", Harness: "antigravity", Model: "gemini-4-neon", Phase: "verify", Verdict: VerdictGreen, DurationSec: 10, TokensIn: 1000, TokensOut: 500, CostUSD: 0.10},
		{SliceID: "S-2", Harness: "antigravity", Model: "gemini-4-neon", Phase: "red", Verdict: VerdictRed, DurationSec: 20, TokensIn: 500, TokensOut: 500, CostUSD: 0.05},
		{SliceID: "S-3", Harness: "opencode", Model: "gemini-4-hydrogen", Phase: "verify", Verdict: VerdictGreen, DurationSec: 30, TokensIn: 2000, TokensOut: 1000, CostUSD: 0.20},
		{SliceID: "S-4", Harness: "codex", Phase: "verify", Verdict: VerdictGreen, DurationSec: 15},
	}
	byModel := BuildTelemetryBreakdown(runs, func(ev RunEvent) string { return ev.Model })
	if len(byModel) != 2 || byModel[0].Key != "gemini-4-neon" || byModel[0].Runs != 2 || byModel[0].GreenRuns != 1 || byModel[0].GreenRate != 0.5 || byModel[0].AvgDurSec != 15 || byModel[0].TotalTokens != 2500 {
		t.Fatalf("unexpected byModel breakdown: %+v", byModel)
	}
	byHarness := BuildTelemetryBreakdown(runs, func(ev RunEvent) string { return ev.Harness })
	if len(byHarness) != 3 || byHarness[0].Key != "antigravity" || byHarness[0].Runs != 2 {
		t.Fatalf("unexpected byHarness breakdown: %+v", byHarness)
	}
}
