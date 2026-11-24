package observer

import (
	"slices"
	"sort"
	"strings"

	"github.com/autox-my/mill/internal/domain"
)

// Report summarizes run ledger failures, amendments, and footprint findings grounded in exact slice IDs.
type Report struct {
	TotalEvents            int              `json:"total_events"`
	RedEvents              int              `json:"red_events"`
	TotalAmendments        int              `json:"total_amendments"`
	TotalFootprintFindings int              `json:"total_footprint_findings"`
	SliceIDs               []string         `json:"slice_ids"`
	Hotspots               []SliceSummary   `json:"hotspots"`
	Patterns               []FailurePattern `json:"patterns"`
}

// SliceSummary aggregates ledger metrics for a single slice.
type SliceSummary struct {
	SliceID           string            `json:"slice_id"`
	Title             string            `json:"title,omitempty"`
	State             domain.SliceState `json:"state,omitempty"`
	TotalRuns         int               `json:"total_runs"`
	RedRuns           int               `json:"red_runs"`
	Amendments        int               `json:"amendments"`
	FootprintFindings int               `json:"footprint_findings"`
	Phases            []string          `json:"phases,omitempty"`
	SampleNotes       []string          `json:"sample_notes,omitempty"`
}

// FailurePattern groups recurring failure characteristics across slices.
type FailurePattern struct {
	Key      string   `json:"key"`
	Count    int      `json:"count"`
	SliceIDs []string `json:"slice_ids"`
}

// HasEvidence reports whether the report contains at least one analyzed slice_id.
func (r Report) HasEvidence() bool {
	return len(r.SliceIDs) > 0
}

// Analyze inspects run ledger events and produces a grounded Report.
func Analyze(events []domain.RunEvent) Report {
	return AnalyzeWithSlices(events, nil)
}

// AnalyzeWithSlices inspects run ledger events enriched with optional slice metadata.
func AnalyzeWithSlices(events []domain.RunEvent, sliceList []domain.Slice) Report {
	if len(events) == 0 {
		return Report{}
	}

	sliceMeta := indexSlices(sliceList)
	bySlice := make(map[string]*sliceAcc)
	byPattern := make(map[string]*patternAcc)

	rep := Report{TotalEvents: len(events)}
	for _, ev := range events {
		if strings.TrimSpace(ev.SliceID) == "" {
			continue
		}
		if ev.Verdict == domain.VerdictRed {
			rep.RedEvents++
		}
		rep.TotalAmendments += ev.Amendments
		rep.TotalFootprintFindings += ev.FootprintFindings

		acc := getOrCreateSliceAcc(bySlice, ev.SliceID, sliceMeta)
		acc.add(ev)
		recordPattern(byPattern, ev)
	}

	rep.SliceIDs = sortedKeys(bySlice)
	rep.Hotspots = buildHotspots(bySlice)
	rep.Patterns = buildPatterns(byPattern)
	return rep
}

func indexSlices(sliceList []domain.Slice) map[string]domain.Slice {
	out := make(map[string]domain.Slice, len(sliceList))
	for _, s := range sliceList {
		out[s.ID] = s
	}
	return out
}

type sliceAcc struct {
	summary SliceSummary
	phases  map[string]bool
	notes   map[string]bool
}

func getOrCreateSliceAcc(m map[string]*sliceAcc, id string, meta map[string]domain.Slice) *sliceAcc {
	if acc, ok := m[id]; ok {
		return acc
	}
	sum := SliceSummary{SliceID: id}
	if s, ok := meta[id]; ok {
		sum.Title = s.Title
		sum.State = s.State
	}
	acc := &sliceAcc{
		summary: sum,
		phases:  make(map[string]bool),
		notes:   make(map[string]bool),
	}
	m[id] = acc
	return acc
}

func (a *sliceAcc) add(ev domain.RunEvent) {
	a.summary.TotalRuns++
	if ev.Verdict == domain.VerdictRed {
		a.summary.RedRuns++
	}
	a.summary.Amendments += ev.Amendments
	a.summary.FootprintFindings += ev.FootprintFindings
	if ph := strings.TrimSpace(ev.Phase); ph != "" {
		a.phases[ph] = true
	}
	if note := strings.TrimSpace(ev.Notes); note != "" && len(a.notes) < 3 {
		a.notes[note] = true
	}
}

func (a *sliceAcc) finalize() SliceSummary {
	out := a.summary
	out.Phases = sortedBoolKeys(a.phases)
	out.SampleNotes = sortedBoolKeys(a.notes)
	return out
}

type patternAcc struct {
	key      string
	count    int
	sliceIDs map[string]bool
}

func recordPattern(m map[string]*patternAcc, ev domain.RunEvent) {
	if ev.Verdict != domain.VerdictRed && ev.Amendments == 0 && ev.FootprintFindings == 0 {
		return
	}
	key := ev.Harness + ":" + ev.Phase
	if ev.ReqDiffDirection != "" {
		key += ":" + ev.ReqDiffDirection
	}
	acc, ok := m[key]
	if !ok {
		acc = &patternAcc{key: key, sliceIDs: make(map[string]bool)}
		m[key] = acc
	}
	acc.count++
	acc.sliceIDs[ev.SliceID] = true
}

func buildHotspots(m map[string]*sliceAcc) []SliceSummary {
	out := make([]SliceSummary, 0, len(m))
	for _, acc := range m {
		out = append(out, acc.finalize())
	}
	sort.Slice(out, func(i, j int) bool {
		si := hotspotScore(out[i])
		sj := hotspotScore(out[j])
		if si != sj {
			return si > sj
		}
		return out[i].SliceID < out[j].SliceID
	})
	return out
}

func hotspotScore(s SliceSummary) int {
	return s.RedRuns*10 + s.FootprintFindings*3 + s.Amendments
}

func buildPatterns(m map[string]*patternAcc) []FailurePattern {
	out := make([]FailurePattern, 0, len(m))
	for _, acc := range m {
		out = append(out, FailurePattern{
			Key:      acc.key,
			Count:    acc.count,
			SliceIDs: sortedBoolKeys(acc.sliceIDs),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func sortedBoolKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
