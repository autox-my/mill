// Package webhooks provides authenticated HTTP webhook ingestion for Forgejo and Pulse.
package webhooks

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/autox-my/mill/internal/domain"
	_ "modernc.org/sqlite"
)

const deliverySchemaSQL = `
CREATE TABLE IF NOT EXISTS webhook_deliveries (
	delivery_id TEXT PRIMARY KEY,
	source TEXT NOT NULL,
	event_type TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	slice_id TEXT NOT NULL DEFAULT '',
	received_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_source_received
	ON webhook_deliveries(source, received_at DESC);
`

// DeliveryRecord represents a persisted webhook delivery for deduplication and audit.
type DeliveryRecord struct {
	DeliveryID string    `json:"delivery_id"`
	Source     string    `json:"source"`
	EventType  string    `json:"event_type"`
	Status     string    `json:"status"`
	SliceID    string    `json:"slice_id,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}

// DeliveryStore persists and queries processed webhook delivery IDs.
type DeliveryStore interface {
	RecordDelivery(ctx context.Context, rec DeliveryRecord) (inserted bool, err error)
	ListDeliveries(ctx context.Context, source string, limit int) ([]DeliveryRecord, error)
}

// SQLiteDeliveryStore implements DeliveryStore backed by SQLite.
type SQLiteDeliveryStore struct {
	db *sql.DB
}

// OpenDeliveryStore opens or initializes the SQLite webhook delivery log at dbPath.
func OpenDeliveryStore(dbPath string) (*SQLiteDeliveryStore, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, fmt.Errorf("%w: db path is required", domain.ErrInvalidInput)
	}
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create delivery db directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open delivery sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := initDeliveryDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteDeliveryStore{db: db}, nil
}

func initDeliveryDB(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("exec %q: %w", p, err)
		}
	}
	if _, err := db.Exec(deliverySchemaSQL); err != nil {
		return fmt.Errorf("apply delivery schema: %w", err)
	}
	return nil
}

// Close closes the underlying SQLite database handle.
func (s *SQLiteDeliveryStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// RecordDelivery inserts a delivery record if its delivery_id has not been seen before.
// It returns inserted=true for a new delivery, or inserted=false for a duplicate.
func (s *SQLiteDeliveryStore) RecordDelivery(ctx context.Context, rec DeliveryRecord) (bool, error) {
	if strings.TrimSpace(rec.DeliveryID) == "" {
		return false, fmt.Errorf("%w: delivery_id is required", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(rec.Source) == "" {
		return false, fmt.Errorf("%w: source is required", domain.ErrInvalidInput)
	}
	ts := rec.ReceivedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	} else {
		ts = ts.UTC()
	}
	const q = `
		INSERT OR IGNORE INTO webhook_deliveries (delivery_id, source, event_type, status, slice_id, received_at)
		VALUES (?, ?, ?, ?, ?, ?)`
	res, err := s.db.ExecContext(ctx, q,
		rec.DeliveryID,
		rec.Source,
		rec.EventType,
		rec.Status,
		rec.SliceID,
		ts.Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, fmt.Errorf("insert delivery: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delivery rows affected: %w", err)
	}
	return affected > 0, nil
}

// ListDeliveries returns recent webhook deliveries, optionally filtered by source.
func (s *SQLiteDeliveryStore) ListDeliveries(ctx context.Context, source string, limit int) ([]DeliveryRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT delivery_id, source, event_type, status, slice_id, received_at FROM webhook_deliveries`
	var args []any
	if src := strings.TrimSpace(source); src != "" {
		query += ` WHERE source = ?`
		args = append(args, src)
	}
	query += ` ORDER BY received_at DESC, delivery_id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()

	var out []DeliveryRecord
	for rows.Next() {
		rec, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanDelivery(rows *sql.Rows) (DeliveryRecord, error) {
	var (
		rec   DeliveryRecord
		rawTS string
	)
	if err := rows.Scan(&rec.DeliveryID, &rec.Source, &rec.EventType, &rec.Status, &rec.SliceID, &rawTS); err != nil {
		return DeliveryRecord{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, rawTS)
	if err != nil {
		return DeliveryRecord{}, fmt.Errorf("parse received_at %q: %w", rawTS, err)
	}
	rec.ReceivedAt = parsed.UTC()
	return rec, nil
}
