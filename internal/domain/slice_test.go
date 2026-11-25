package domain

import (
	"errors"
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
