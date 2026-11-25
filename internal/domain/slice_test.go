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
}
