// Package domain defines the core slice board, lease, and run ledger types and invariants.
// It has zero external dependencies so outer adapters depend inward on it.
package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// SliceState represents the lifecycle stage of a work slice.
type SliceState string

const (
	StateSpec          SliceState = "spec"
	StateLocked        SliceState = "locked"
	StateClaimed       SliceState = "claimed"
	StateRed           SliceState = "red"
	StateGreen         SliceState = "green"
	StateInReview      SliceState = "in_review"
	StateReadyForHuman SliceState = "ready_for_human"
	StateMerged        SliceState = "merged"
)

// Verdict represents a machine or review check outcome.
type Verdict string

const (
	VerdictPending     Verdict = "pending"
	VerdictGreen       Verdict = "GREEN"
	VerdictRed         Verdict = "RED"
	VerdictUndecidable Verdict = "UNDECIDABLE"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrAlreadyClaimed    = errors.New("slice already claimed by active lease")
	ErrNotLeaseOwner     = errors.New("caller does not hold an active lease")
	ErrInvalidTransition = errors.New("invalid slice state transition")
	ErrInvalidInput      = errors.New("invalid input")
)

var allowedTransitions = map[SliceState][]SliceState{
	StateSpec:          {StateLocked, StateClaimed},
	StateLocked:        {StateClaimed},
	StateClaimed:       {StateRed, StateGreen, StateInReview, StateSpec},
	StateRed:           {StateGreen, StateClaimed},
	StateGreen:         {StateInReview, StateRed, StateReadyForHuman, StateMerged},
	StateInReview:      {StateReadyForHuman, StateRed, StateGreen, StateMerged},
	StateReadyForHuman: {StateMerged, StateInReview},
	StateMerged:        {},
}

var validVerdicts = map[Verdict]bool{
	VerdictPending:     true,
	VerdictGreen:       true,
	VerdictRed:         true,
	VerdictUndecidable: true,
}

// Slice is a schema-validated coordination card tracked on the mill board.
type Slice struct {
	ID             string     `json:"id"`
	Repo           string     `json:"repo"`
	Title          string     `json:"title"`
	Claims         []string   `json:"claims"`
	IntentLock     string     `json:"intent_lock,omitempty"`
	Branch         string     `json:"branch,omitempty"`
	State          SliceState `json:"state"`
	Owner          string     `json:"owner,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	Attempt        int        `json:"attempt"`
	LastVerdict    Verdict    `json:"last_verdict"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// RunEvent records a single harness execution or verification outcome for a slice.
type RunEvent struct {
	ID                int64     `json:"id"`
	SliceID           string    `json:"slice_id"`
	Harness           string    `json:"harness"`
	Model             string    `json:"model,omitempty"`
	Phase             string    `json:"phase"`
	Verdict           Verdict   `json:"verdict"`
	DurationSec       int       `json:"duration_sec"`
	Amendments        int       `json:"amendments"`
	FootprintFindings int       `json:"footprint_findings"`
	ReqDiffDirection  string    `json:"req_diff_direction,omitempty"`
	Notes             string    `json:"notes,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// StatsSummary aggregates slice counts and run ledger metrics.
type StatsSummary struct {
	TotalSlices     int                `json:"total_slices"`
	ByState         map[SliceState]int `json:"by_state"`
	TotalRuns       int                `json:"total_runs"`
	GreenRuns       int                `json:"green_runs"`
	RedRuns         int                `json:"red_runs"`
	TotalAmendments int                `json:"total_amendments"`
	TotalFindings   int                `json:"total_findings"`
}

// ValidState reports whether s is a recognized slice lifecycle state.
func ValidState(s SliceState) bool {
	_, ok := allowedTransitions[s]
	return ok
}

// ValidTransition reports whether moving from `from` to `to` is permitted.
func ValidTransition(from, to SliceState) bool {
	nexts, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	return slices.Contains(nexts, to)
}

// ValidVerdict reports whether v is a recognized verdict value.
func ValidVerdict(v Verdict) bool {
	return validVerdicts[v]
}

// ValidateSliceCreate checks required fields for creating a new slice.
func ValidateSliceCreate(s Slice) error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("%w: slice id is required", ErrInvalidInput)
	}
	if strings.TrimSpace(s.Repo) == "" {
		return fmt.Errorf("%w: repo is required", ErrInvalidInput)
	}
	if strings.TrimSpace(s.Title) == "" {
		return fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	if s.State != "" && !ValidState(s.State) {
		return fmt.Errorf("%w: unknown state %q", ErrInvalidInput, s.State)
	}
	if s.LastVerdict != "" && !ValidVerdict(s.LastVerdict) {
		return fmt.Errorf("%w: unknown verdict %q", ErrInvalidInput, s.LastVerdict)
	}
	return nil
}

// ValidateClaim checks required fields for claiming or renewing a slice lease.
func ValidateClaim(id, owner string, ttl time.Duration) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: slice id is required", ErrInvalidInput)
	}
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("%w: owner is required", ErrInvalidInput)
	}
	if ttl <= 0 {
		return fmt.Errorf("%w: ttl must be positive", ErrInvalidInput)
	}
	return nil
}

// ValidateRunEvent checks required fields and non-negative counters on a run event.
func ValidateRunEvent(ev RunEvent) error {
	if strings.TrimSpace(ev.SliceID) == "" {
		return fmt.Errorf("%w: slice_id is required", ErrInvalidInput)
	}
	if strings.TrimSpace(ev.Harness) == "" {
		return fmt.Errorf("%w: harness is required", ErrInvalidInput)
	}
	if strings.TrimSpace(ev.Phase) == "" {
		return fmt.Errorf("%w: phase is required", ErrInvalidInput)
	}
	if !ValidVerdict(ev.Verdict) {
		return fmt.Errorf("%w: invalid verdict %q", ErrInvalidInput, ev.Verdict)
	}
	if ev.DurationSec < 0 || ev.Amendments < 0 || ev.FootprintFindings < 0 {
		return fmt.Errorf("%w: counters must be non-negative", ErrInvalidInput)
	}
	return nil
}
