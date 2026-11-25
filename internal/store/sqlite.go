// Package store implements persistent SQLite storage for slices, leases, and run events.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/autox-my/mill/internal/domain"
	_ "modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS slices (
	id TEXT PRIMARY KEY,
	repo TEXT NOT NULL,
	title TEXT NOT NULL,
	claims_json TEXT NOT NULL DEFAULT '[]',
	intent_lock TEXT NOT NULL DEFAULT '',
	branch TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	lease_expires_at TEXT,
	attempt INTEGER NOT NULL DEFAULT 0,
	last_verdict TEXT NOT NULL DEFAULT 'pending',
	risk TEXT NOT NULL DEFAULT 'low',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS run_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slice_id TEXT NOT NULL REFERENCES slices(id) ON DELETE CASCADE,
	harness TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	phase TEXT NOT NULL,
	verdict TEXT NOT NULL,
	duration_sec INTEGER NOT NULL DEFAULT 0,
	amendments INTEGER NOT NULL DEFAULT 0,
	footprint_findings INTEGER NOT NULL DEFAULT 0,
	req_diff_direction TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_run_events_slice_id ON run_events(slice_id, id DESC);
`

// Store persists mill slices and run ledger events in SQLite.
type Store struct {
	db *sql.DB
}

// Open opens or initializes the SQLite database at path.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: db path is required", domain.ErrInvalidInput)
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single connection serializes writers cleanly in pure-Go SQLite without SQLITE_BUSY flakes.
	db.SetMaxOpenConns(1)

	if err := initDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func initDB(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("exec %q: %w", p, err)
		}
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := ensureSliceRiskColumn(db); err != nil {
		return fmt.Errorf("migrate slice risk: %w", err)
	}
	return nil
}

func ensureSliceRiskColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(slices)`)
	if err != nil {
		return err
	}
	hasRisk := false
	for rows.Next() {
		var (
			cid     int
			name    string
			colType string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "risk" {
			hasRisk = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasRisk {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE slices ADD COLUMN risk TEXT NOT NULL DEFAULT 'low'`)
	return err
}

// Close closes the underlying SQLite handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateSlice inserts a new slice into the board.
func (s *Store) CreateSlice(ctx context.Context, sl domain.Slice) error {
	if err := domain.ValidateSliceCreate(sl); err != nil {
		return err
	}
	if sl.State == "" {
		sl.State = domain.StateSpec
	}
	if sl.LastVerdict == "" {
		sl.LastVerdict = domain.VerdictPending
	}
	sl.Risk = domain.NormalizeRisk(sl.Risk)
	now := time.Now().UTC()
	if sl.CreatedAt.IsZero() {
		sl.CreatedAt = now
	}
	if sl.UpdatedAt.IsZero() {
		sl.UpdatedAt = sl.CreatedAt
	}
	claimsJSON, err := marshalClaims(sl.Claims)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO slices (id, repo, title, claims_json, intent_lock, branch, state, owner, lease_expires_at, attempt, last_verdict, risk, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, q,
		sl.ID, sl.Repo, sl.Title, claimsJSON, sl.IntentLock, sl.Branch,
		string(sl.State), sl.Owner, formatOptTime(sl.LeaseExpiresAt), sl.Attempt,
		string(sl.LastVerdict), string(sl.Risk), formatTime(sl.CreatedAt), formatTime(sl.UpdatedAt),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: slice %s", domain.ErrAlreadyExists, sl.ID)
		}
		return fmt.Errorf("insert slice: %w", err)
	}
	return nil
}

// GetSlice fetches a slice by ID.
func (s *Store) GetSlice(ctx context.Context, id string) (domain.Slice, error) {
	return getSliceTx(ctx, s.db, id)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getSliceTx(ctx context.Context, q queryRower, id string) (domain.Slice, error) {
	const stmt = `
		SELECT id, repo, title, claims_json, intent_lock, branch, state, owner, lease_expires_at, attempt, last_verdict, risk, created_at, updated_at
		FROM slices WHERE id = ?`
	sl, err := scanSlice(q.QueryRowContext(ctx, stmt, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Slice{}, fmt.Errorf("%w: slice %s", domain.ErrNotFound, id)
	}
	if err != nil {
		return domain.Slice{}, fmt.Errorf("get slice %s: %w", id, err)
	}
	return sl, nil
}

// ListSlices returns slices ordered by creation time, optionally filtered by state.
func (s *Store) ListSlices(ctx context.Context, stateFilter string) ([]domain.Slice, error) {
	query := `
		SELECT id, repo, title, claims_json, intent_lock, branch, state, owner, lease_expires_at, attempt, last_verdict, risk, created_at, updated_at
		FROM slices`
	var args []any
	if strings.TrimSpace(stateFilter) != "" {
		if !domain.ValidState(domain.SliceState(stateFilter)) {
			return nil, fmt.Errorf("%w: unknown state %q", domain.ErrInvalidInput, stateFilter)
		}
		query += " WHERE state = ?"
		args = append(args, stateFilter)
	}
	query += " ORDER BY created_at ASC, id ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list slices: %w", err)
	}
	defer rows.Close()

	var out []domain.Slice
	for rows.Next() {
		sl, err := scanSlice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan slice: %w", err)
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// ClaimSlice atomically acquires or renews a lease on a slice for owner.
func (s *Store) ClaimSlice(ctx context.Context, id, owner string, ttl time.Duration, now time.Time) (domain.Slice, error) {
	if err := domain.ValidateClaim(id, owner, ttl); err != nil {
		return domain.Slice{}, err
	}
	nowUTC := normalizeTime(now)
	var updated domain.Slice

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		sl, err := getSliceTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if hasActiveOtherLease(sl, owner, nowUTC) {
			return fmt.Errorf("%w: held by %s until %s", domain.ErrAlreadyClaimed, sl.Owner, sl.LeaseExpiresAt.Format(time.RFC3339))
		}
		nextState := sl.State
		if sl.State == domain.StateSpec || sl.State == domain.StateLocked {
			nextState = domain.StateClaimed
		}
		exp := nowUTC.Add(ttl)
		sl.Owner = owner
		sl.LeaseExpiresAt = &exp
		sl.Attempt++
		sl.State = nextState
		sl.UpdatedAt = nowUTC

		const upd = `UPDATE slices SET owner = ?, lease_expires_at = ?, attempt = ?, state = ?, updated_at = ? WHERE id = ?`
		if _, err := tx.ExecContext(ctx, upd, sl.Owner, formatTime(exp), sl.Attempt, string(sl.State), formatTime(sl.UpdatedAt), sl.ID); err != nil {
			return fmt.Errorf("update claim: %w", err)
		}
		updated = sl
		return nil
	})
	return updated, err
}

// RenewLease extends an active lease held by owner.
func (s *Store) RenewLease(ctx context.Context, id, owner string, ttl time.Duration, now time.Time) (domain.Slice, error) {
	if err := domain.ValidateClaim(id, owner, ttl); err != nil {
		return domain.Slice{}, err
	}
	nowUTC := normalizeTime(now)
	var updated domain.Slice

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		sl, err := getSliceTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !isActiveLeaseOwner(sl, owner, nowUTC) {
			return fmt.Errorf("%w: owner=%q", domain.ErrNotLeaseOwner, sl.Owner)
		}
		exp := nowUTC.Add(ttl)
		sl.LeaseExpiresAt = &exp
		sl.UpdatedAt = nowUTC

		const upd = `UPDATE slices SET lease_expires_at = ?, updated_at = ? WHERE id = ?`
		if _, err := tx.ExecContext(ctx, upd, formatTime(exp), formatTime(sl.UpdatedAt), sl.ID); err != nil {
			return fmt.Errorf("renew lease: %w", err)
		}
		updated = sl
		return nil
	})
	return updated, err
}

func validateSliceUpdate(next domain.SliceState, verdict domain.Verdict, risk domain.RiskLevel) error {
	if next == "" && risk == "" {
		return fmt.Errorf("%w: state or risk is required", domain.ErrInvalidInput)
	}
	if next != "" && !domain.ValidState(next) {
		return fmt.Errorf("%w: unknown state %q", domain.ErrInvalidInput, next)
	}
	if verdict != "" && !domain.ValidVerdict(verdict) {
		return fmt.Errorf("%w: unknown verdict %q", domain.ErrInvalidInput, verdict)
	}
	if risk != "" && !domain.ValidRisk(risk) {
		return fmt.Errorf("%w: unknown risk %q", domain.ErrInvalidInput, risk)
	}
	return nil
}

// UpdateSliceState transitions a slice to next state and optionally records a verdict and merge risk.
// Merge risk is clamped to at least the deterministic floor implied by recorded run events.
func (s *Store) UpdateSliceState(ctx context.Context, id string, next domain.SliceState, verdict domain.Verdict, risk domain.RiskLevel, now time.Time) (domain.Slice, error) {
	if err := validateSliceUpdate(next, verdict, risk); err != nil {
		return domain.Slice{}, err
	}
	nowUTC := normalizeTime(now)
	var updated domain.Slice

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		sl, err := getSliceTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if next != "" {
			if !domain.ValidTransition(sl.State, next) {
				return fmt.Errorf("%w: %s -> %s", domain.ErrInvalidTransition, sl.State, next)
			}
			sl.State = next
		}
		if verdict != "" {
			sl.LastVerdict = verdict
		}
		floor, err := sliceRiskFloorTx(ctx, tx, sl.ID)
		if err != nil {
			return err
		}
		candidate := sl.Risk
		if risk != "" {
			candidate = risk
		}
		sl.Risk = domain.MaxRisk(candidate, floor)
		sl.UpdatedAt = nowUTC

		const upd = `UPDATE slices SET state = ?, last_verdict = ?, risk = ?, updated_at = ? WHERE id = ?`
		if _, err := tx.ExecContext(ctx, upd, string(sl.State), string(sl.LastVerdict), string(sl.Risk), formatTime(sl.UpdatedAt), sl.ID); err != nil {
			return fmt.Errorf("update slice state: %w", err)
		}
		updated = sl
		return nil
	})
	return updated, err
}

func sliceRiskFloorTx(ctx context.Context, tx *sql.Tx, sliceID string) (domain.RiskLevel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT verdict, amendments, footprint_findings, req_diff_direction FROM run_events WHERE slice_id = ?`, sliceID)
	if err != nil {
		return domain.RiskLow, fmt.Errorf("query risk floor: %w", err)
	}
	defer rows.Close()

	floor := domain.RiskLow
	for rows.Next() {
		var (
			ev         domain.RunEvent
			verdictStr string
		)
		if err := rows.Scan(&verdictStr, &ev.Amendments, &ev.FootprintFindings, &ev.ReqDiffDirection); err != nil {
			return domain.RiskLow, fmt.Errorf("scan risk floor: %w", err)
		}
		ev.Verdict = domain.Verdict(verdictStr)
		floor = domain.MaxRisk(floor, domain.RunRiskFloor(ev))
	}
	return floor, rows.Err()
}

// RecordRun appends a run telemetry event for an existing slice and enforces the deterministic risk floor.
func (s *Store) RecordRun(ctx context.Context, ev domain.RunEvent) (domain.RunEvent, error) {
	if err := domain.ValidateRunEvent(ev); err != nil {
		return domain.RunEvent{}, err
	}
	ev.CreatedAt = normalizeTime(ev.CreatedAt)

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		sl, err := getSliceTx(ctx, tx, ev.SliceID)
		if err != nil {
			return err
		}
		const ins = `
			INSERT INTO run_events (slice_id, harness, model, phase, verdict, duration_sec, amendments, footprint_findings, req_diff_direction, notes, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		res, err := tx.ExecContext(ctx, ins,
			ev.SliceID, ev.Harness, ev.Model, ev.Phase, string(ev.Verdict),
			ev.DurationSec, ev.Amendments, ev.FootprintFindings, ev.ReqDiffDirection,
			ev.Notes, formatTime(ev.CreatedAt),
		)
		if err != nil {
			return fmt.Errorf("insert run event: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("last insert id: %w", err)
		}
		ev.ID = id

		newRisk := domain.MaxRisk(sl.Risk, domain.RunRiskFloor(ev))
		if newRisk != sl.Risk {
			if _, err := tx.ExecContext(ctx, `UPDATE slices SET risk = ?, updated_at = ? WHERE id = ?`, string(newRisk), formatTime(ev.CreatedAt), sl.ID); err != nil {
				return fmt.Errorf("raise slice risk: %w", err)
			}
		}
		return nil
	})
	return ev, err
}

// ListRuns returns recorded run events, optionally filtered by sliceID.
func (s *Store) ListRuns(ctx context.Context, sliceID string, limit int) ([]domain.RunEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, slice_id, harness, model, phase, verdict, duration_sec, amendments, footprint_findings, req_diff_direction, notes, created_at
		FROM run_events`
	var args []any
	if strings.TrimSpace(sliceID) != "" {
		query += " WHERE slice_id = ?"
		args = append(args, sliceID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var out []domain.RunEvent
	for rows.Next() {
		ev, err := scanRunEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run event: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Stats returns aggregated slice and run ledger counts.
func (s *Store) Stats(ctx context.Context) (domain.StatsSummary, error) {
	summary := domain.StatsSummary{ByState: make(map[domain.SliceState]int)}
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM slices GROUP BY state`)
	if err != nil {
		return summary, fmt.Errorf("query slice stats: %w", err)
	}
	for rows.Next() {
		var st string
		var cnt int
		if err := rows.Scan(&st, &cnt); err != nil {
			_ = rows.Close()
			return summary, err
		}
		summary.ByState[domain.SliceState(st)] = cnt
		summary.TotalSlices += cnt
	}
	if err := rows.Close(); err != nil {
		return summary, err
	}

	const runQ = `
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN verdict = 'GREEN' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN verdict = 'RED' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(amendments), 0),
			COALESCE(SUM(footprint_findings), 0)
		FROM run_events`
	if err := s.db.QueryRowContext(ctx, runQ).Scan(
		&summary.TotalRuns, &summary.GreenRuns, &summary.RedRuns,
		&summary.TotalAmendments, &summary.TotalFindings,
	); err != nil {
		return summary, fmt.Errorf("query run stats: %w", err)
	}
	return summary, nil
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func hasActiveOtherLease(sl domain.Slice, owner string, now time.Time) bool {
	if sl.Owner == "" || sl.Owner == owner || sl.LeaseExpiresAt == nil {
		return false
	}
	return sl.LeaseExpiresAt.After(now)
}

func isActiveLeaseOwner(sl domain.Slice, owner string, now time.Time) bool {
	if sl.Owner == "" || sl.Owner != owner || sl.LeaseExpiresAt == nil {
		return false
	}
	return sl.LeaseExpiresAt.After(now)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSlice(sc scanner) (domain.Slice, error) {
	var (
		sl         domain.Slice
		claimsJSON string
		stateStr   string
		verdictStr string
		riskStr    string
		leaseStr   sql.NullString
		createdStr string
		updatedStr string
	)
	err := sc.Scan(
		&sl.ID, &sl.Repo, &sl.Title, &claimsJSON, &sl.IntentLock, &sl.Branch,
		&stateStr, &sl.Owner, &leaseStr, &sl.Attempt, &verdictStr, &riskStr, &createdStr, &updatedStr,
	)
	if err != nil {
		return domain.Slice{}, err
	}
	sl.State = domain.SliceState(stateStr)
	sl.LastVerdict = domain.Verdict(verdictStr)
	sl.Risk = domain.NormalizeRisk(domain.RiskLevel(riskStr))
	if err := json.Unmarshal([]byte(claimsJSON), &sl.Claims); err != nil {
		return domain.Slice{}, fmt.Errorf("decode claims: %w", err)
	}
	if leaseStr.Valid && leaseStr.String != "" {
		t, err := time.Parse(time.RFC3339Nano, leaseStr.String)
		if err != nil {
			return domain.Slice{}, fmt.Errorf("parse lease_expires_at: %w", err)
		}
		sl.LeaseExpiresAt = &t
	}
	if sl.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr); err != nil {
		return domain.Slice{}, fmt.Errorf("parse created_at: %w", err)
	}
	if sl.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedStr); err != nil {
		return domain.Slice{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return sl, nil
}

func scanRunEvent(sc scanner) (domain.RunEvent, error) {
	var (
		ev         domain.RunEvent
		verdictStr string
		createdStr string
	)
	err := sc.Scan(
		&ev.ID, &ev.SliceID, &ev.Harness, &ev.Model, &ev.Phase, &verdictStr,
		&ev.DurationSec, &ev.Amendments, &ev.FootprintFindings, &ev.ReqDiffDirection,
		&ev.Notes, &createdStr,
	)
	if err != nil {
		return domain.RunEvent{}, err
	}
	ev.Verdict = domain.Verdict(verdictStr)
	ev.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("parse created_at: %w", err)
	}
	return ev, nil
}

func marshalClaims(claims []string) (string, error) {
	if claims == nil {
		claims = []string{}
	}
	b, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	return string(b), nil
}

func normalizeTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func formatOptTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*t), Valid: true}
}
