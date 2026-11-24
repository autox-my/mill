// Package httpapi exposes a bearer-authenticated JSON HTTP API over the mill store.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/autox-my/mill/internal/domain"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// Store defines the persistence operations required by the HTTP API.
type Store interface {
	CreateSlice(ctx context.Context, sl domain.Slice) error
	GetSlice(ctx context.Context, id string) (domain.Slice, error)
	ListSlices(ctx context.Context, stateFilter string) ([]domain.Slice, error)
	ClaimSlice(ctx context.Context, id, owner string, ttl time.Duration, now time.Time) (domain.Slice, error)
	RenewLease(ctx context.Context, id, owner string, ttl time.Duration, now time.Time) (domain.Slice, error)
	UpdateSliceState(ctx context.Context, id string, next domain.SliceState, verdict domain.Verdict, now time.Time) (domain.Slice, error)
	RecordRun(ctx context.Context, ev domain.RunEvent) (domain.RunEvent, error)
	ListRuns(ctx context.Context, sliceID string, limit int) ([]domain.RunEvent, error)
	Stats(ctx context.Context) (domain.StatsSummary, error)
}

// Config configures the HTTP API handler.
type Config struct {
	Store    Store
	APIToken string
}

type server struct {
	store    Store
	apiToken string
}

// NewHandler constructs a bearer-authenticated HTTP handler for the mill API.
// If apiToken or store is empty/nil, all requests fail safe with 503 Service Unavailable.
func NewHandler(store Store, apiToken string) http.Handler {
	return NewHandlerWithConfig(Config{
		Store:    store,
		APIToken: apiToken,
	})
}

// NewHandlerWithConfig constructs a bearer-authenticated HTTP handler from Config.
func NewHandlerWithConfig(cfg Config) http.Handler {
	s := &server{
		store:    cfg.Store,
		apiToken: cfg.APIToken,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slices", s.handleListSlices)
	mux.HandleFunc("GET /slices/{id}", s.handleGetSlice)
	mux.HandleFunc("POST /slices", s.handleCreateSlice)
	mux.HandleFunc("POST /slices/{id}/claim", s.handleClaimSlice)
	mux.HandleFunc("POST /slices/{id}/renew", s.handleRenewLease)
	mux.HandleFunc("POST /slices/{id}/update", s.handleUpdateSlice)
	mux.HandleFunc("GET /runs", s.handleListRuns)
	mux.HandleFunc("POST /runs", s.handleRecordRun)
	mux.HandleFunc("GET /stats", s.handleStats)

	return s.authMiddleware(mux)
}

func (s *server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.apiToken == "" || s.store == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("api server is not configured"))
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.apiToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleListSlices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stateFilter := q.Get("state")
	repoFilter := strings.TrimSpace(q.Get("repo"))

	items, err := s.store.ListSlices(r.Context(), stateFilter)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]domain.Slice, 0, len(items))
	for _, sl := range items {
		if repoFilter == "" || sl.Repo == repoFilter {
			out = append(out, sl)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleGetSlice(w http.ResponseWriter, r *http.Request) {
	sl, err := s.store.GetSlice(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sl)
}

func (s *server) handleCreateSlice(w http.ResponseWriter, r *http.Request) {
	var sl domain.Slice
	if !decodeJSON(w, r, &sl) {
		return
	}
	if err := s.store.CreateSlice(r.Context(), sl); err != nil {
		writeError(w, err)
		return
	}
	created, err := s.store.GetSlice(r.Context(), sl.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

type leaseRequest struct {
	Owner      string `json:"owner"`
	TTLSeconds int    `json:"ttl_seconds"`
	TTL        string `json:"ttl"`
}

func (s *server) handleClaimSlice(w http.ResponseWriter, r *http.Request) {
	s.handleLeaseOp(w, r, s.store.ClaimSlice)
}

func (s *server) handleRenewLease(w http.ResponseWriter, r *http.Request) {
	s.handleLeaseOp(w, r, s.store.RenewLease)
}

type leaseFunc func(ctx context.Context, id, owner string, ttl time.Duration, now time.Time) (domain.Slice, error)

func (s *server) handleLeaseOp(w http.ResponseWriter, r *http.Request, op leaseFunc) {
	var req leaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ttl, err := parseTTL(req.TTLSeconds, req.TTL)
	if err != nil {
		writeError(w, err)
		return
	}
	updated, err := op(r.Context(), r.PathValue("id"), req.Owner, ttl, time.Now().UTC())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

type updateRequest struct {
	State   domain.SliceState `json:"state"`
	Verdict domain.Verdict    `json:"verdict"`
}

func (s *server) handleUpdateSlice(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	updated, err := s.store.UpdateSliceState(r.Context(), r.PathValue("id"), req.State, req.Verdict, time.Now().UTC())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sliceID := q.Get("slice_id")
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	runs, err := s.store.ListRuns(r.Context(), sliceID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if runs == nil {
		runs = []domain.RunEvent{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *server) handleRecordRun(w http.ResponseWriter, r *http.Request) {
	var ev domain.RunEvent
	if !decodeJSON(w, r, &ev) {
		return
	}
	recorded, err := s.store.RecordRun(r.Context(), ev)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, recorded)
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func parseTTL(ttlSeconds int, ttlStr string) (time.Duration, error) {
	if strings.TrimSpace(ttlStr) != "" {
		d, err := time.ParseDuration(ttlStr)
		if err != nil {
			return 0, fmt.Errorf("%w: invalid ttl %q", domain.ErrInvalidInput, ttlStr)
		}
		return d, nil
	}
	return time.Duration(ttlSeconds) * time.Second, nil
}

func parseLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid limit %q", domain.ErrInvalidInput, raw)
	}
	return n, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid json body"))
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, statusForError(err), errorBody(err.Error()))
}

var errorStatusMap = []struct {
	target error
	status int
}{
	{domain.ErrNotFound, http.StatusNotFound},
	{domain.ErrAlreadyExists, http.StatusConflict},
	{domain.ErrAlreadyClaimed, http.StatusConflict},
	{domain.ErrInvalidTransition, http.StatusConflict},
	{domain.ErrNotLeaseOwner, http.StatusForbidden},
	{domain.ErrInvalidInput, http.StatusBadRequest},
}

func statusForError(err error) int {
	for _, entry := range errorStatusMap {
		if errors.Is(err, entry.target) {
			return entry.status
		}
	}
	return http.StatusInternalServerError
}

func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
