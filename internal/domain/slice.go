// Package domain defines the core slice board, lease, and run ledger types and invariants.
// It has zero external dependencies so outer adapters depend inward on it.
package domain

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
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

// RiskLevel represents the human-review merge risk of a slice.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
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
	Risk           RiskLevel  `json:"risk"`
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
	TokensIn          int       `json:"tokens_in,omitempty"`
	TokensOut         int       `json:"tokens_out,omitempty"`
	CostUSD           float64   `json:"cost_usd,omitempty"`
	Amendments        int       `json:"amendments"`
	FootprintFindings int       `json:"footprint_findings"`
	ReqDiffDirection  string    `json:"req_diff_direction,omitempty"`
	Notes             string    `json:"notes,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// CycleTimeStats summarizes created-to-merged duration across merged slices.
type CycleTimeStats struct {
	MergedCount int     `json:"merged_count"`
	AvgSec      float64 `json:"avg_sec"`
	P50Sec      float64 `json:"p50_sec"`
	MaxSec      int     `json:"max_sec"`
}

// TelemetryBreakdown aggregates run count, green rate, duration, tokens, and spend for a key.
type TelemetryBreakdown struct {
	Key         string  `json:"key"`
	Runs        int     `json:"runs"`
	GreenRuns   int     `json:"green_runs"`
	GreenRate   float64 `json:"green_rate"`
	AvgDurSec   float64 `json:"avg_duration_s"`
	TotalTokens int     `json:"total_tokens"`
	TotalCost   float64 `json:"total_cost_usd"`
}

// StatsSummary aggregates slice counts and run ledger metrics.
type StatsSummary struct {
	TotalSlices         int                  `json:"total_slices"`
	ByState             map[SliceState]int   `json:"by_state"`
	TotalRuns           int                  `json:"total_runs"`
	GreenRuns           int                  `json:"green_runs"`
	RedRuns             int                  `json:"red_runs"`
	TotalAmendments     int                  `json:"total_amendments"`
	TotalFindings       int                  `json:"total_findings"`
	TotalTokensIn       int                  `json:"total_tokens_in"`
	TotalTokensOut      int                  `json:"total_tokens_out"`
	TotalCostUSD        float64              `json:"total_cost_usd"`
	AvgCostPerMergedUSD float64              `json:"avg_cost_per_merged_usd"`
	AvgTokensPerMerged  float64              `json:"avg_tokens_per_merged"`
	CycleTime           CycleTimeStats       `json:"cycle_time"`
	ByModel             []TelemetryBreakdown `json:"by_model,omitempty"`
	ByHarness           []TelemetryBreakdown `json:"by_harness,omitempty"`
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

// ValidRisk reports whether r is a recognized merge risk level.
func ValidRisk(r RiskLevel) bool {
	return r == RiskLow || r == RiskMedium || r == RiskHigh
}

// NormalizeRisk returns r when valid, or RiskLow as the default.
func NormalizeRisk(r RiskLevel) RiskLevel {
	if ValidRisk(r) {
		return r
	}
	return RiskLow
}

// RiskRank returns a numeric ordering weight for comparing risk levels.
func RiskRank(r RiskLevel) int {
	switch r {
	case RiskHigh:
		return 3
	case RiskMedium:
		return 2
	default:
		return 1
	}
}

// MaxRisk returns the higher of two risk levels, normalizing unknown values to RiskLow.
func MaxRisk(a, b RiskLevel) RiskLevel {
	na, nb := NormalizeRisk(a), NormalizeRisk(b)
	if RiskRank(nb) > RiskRank(na) {
		return nb
	}
	return na
}

// RunRiskFloor computes the minimum deterministic merge risk implied by a single run event.
func RunRiskFloor(ev RunEvent) RiskLevel {
	dir := strings.ToLower(strings.TrimSpace(ev.ReqDiffDirection))
	if ev.FootprintFindings > 0 || dir == "breaking" || dir == "loosening" {
		return RiskHigh
	}
	if ev.Amendments > 0 || ev.Verdict == VerdictRed || ev.Verdict == VerdictUndecidable {
		return RiskMedium
	}
	return RiskLow
}

// ComputeRiskFloor folds a slice's run history into its highest deterministic risk floor.
func ComputeRiskFloor(runs []RunEvent) RiskLevel {
	floor := RiskLow
	for _, ev := range runs {
		floor = MaxRisk(floor, RunRiskFloor(ev))
	}
	return floor
}

// EffectiveRisk clamps current risk to at least the deterministic floor from runs.
func EffectiveRisk(current RiskLevel, runs []RunEvent) RiskLevel {
	return MaxRisk(current, ComputeRiskFloor(runs))
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
	if s.Risk != "" && !ValidRisk(s.Risk) {
		return fmt.Errorf("%w: unknown risk %q", ErrInvalidInput, s.Risk)
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
	if ev.DurationSec < 0 || ev.Amendments < 0 || ev.FootprintFindings < 0 || ev.TokensIn < 0 || ev.TokensOut < 0 {
		return fmt.Errorf("%w: counters must be non-negative", ErrInvalidInput)
	}
	if ev.CostUSD < 0 || math.IsNaN(ev.CostUSD) || math.IsInf(ev.CostUSD, 0) {
		return fmt.Errorf("%w: cost_usd must be non-negative and finite", ErrInvalidInput)
	}
	return nil
}

// ComputeCycleTime computes average, median (p50), and max cycle time in seconds for merged slices.
func ComputeCycleTime(sliceList []Slice) CycleTimeStats {
	var durations []int
	var total int
	for _, s := range sliceList {
		if s.State != StateMerged || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
			continue
		}
		sec := int(s.UpdatedAt.Sub(s.CreatedAt).Seconds())
		if sec < 0 {
			sec = 0
		}
		durations = append(durations, sec)
		total += sec
	}
	n := len(durations)
	if n == 0 {
		return CycleTimeStats{}
	}
	slices.Sort(durations)
	p50 := float64(durations[n/2])
	if n%2 == 0 {
		p50 = float64(durations[n/2-1]+durations[n/2]) / 2
	}
	return CycleTimeStats{
		MergedCount: n,
		AvgSec:      float64(total) / float64(n),
		P50Sec:      p50,
		MaxSec:      durations[n-1],
	}
}

type telemetryAcc struct {
	key         string
	runs        int
	greenRuns   int
	totalDurSec int
	totalTokens int
	totalCost   float64
}

// BuildTelemetryBreakdown groups run events by keyFn and returns sorted breakdown metrics.
func BuildTelemetryBreakdown(events []RunEvent, keyFn func(RunEvent) string) []TelemetryBreakdown {
	accMap := make(map[string]*telemetryAcc)
	for _, ev := range events {
		k := strings.TrimSpace(keyFn(ev))
		if k == "" {
			continue
		}
		acc := accMap[k]
		if acc == nil {
			acc = &telemetryAcc{key: k}
			accMap[k] = acc
		}
		acc.runs++
		if ev.Verdict == VerdictGreen {
			acc.greenRuns++
		}
		acc.totalDurSec += ev.DurationSec
		acc.totalTokens += ev.TokensIn + ev.TokensOut
		acc.totalCost += ev.CostUSD
	}
	out := make([]TelemetryBreakdown, 0, len(accMap))
	for _, acc := range accMap {
		out = append(out, TelemetryBreakdown{
			Key:         acc.key,
			Runs:        acc.runs,
			GreenRuns:   acc.greenRuns,
			GreenRate:   float64(acc.greenRuns) / float64(acc.runs),
			AvgDurSec:   float64(acc.totalDurSec) / float64(acc.runs),
			TotalTokens: acc.totalTokens,
			TotalCost:   acc.totalCost,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Key < out[j].Key
	})
	return out
}
