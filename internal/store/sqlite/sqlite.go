// Package sqlite is the default Store backed by a local SQLite file via the
// pure-Go modernc.org/sqlite driver (no CGO, so cross-OS builds stay trivial).
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nachmore/commstats/internal/store"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS metrics (
	source     TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	day        TEXT    NOT NULL,           -- RFC3339 midnight-local
	dimensions TEXT    NOT NULL,           -- canonical JSON, '{}' when none
	value      REAL    NOT NULL,
	updated_at TEXT    NOT NULL,
	PRIMARY KEY (source, name, day, dimensions)
);
`

// Open opens (creating if needed) the SQLite store at path and applies schema.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Upsert replaces each record's value on its natural key. Absolute counts mean
// a same-day re-run overwrites rather than accumulates.
func (s *Store) Upsert(ctx context.Context, recs []store.Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO metrics (source, name, day, dimensions, value, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, name, day, dimensions)
		DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range recs {
		dims, err := canonicalDims(r.Dimensions)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx,
			r.Source, r.Name, r.Day.Format(time.RFC3339), dims, r.Value, now,
		); err != nil {
			return fmt.Errorf("upsert %s/%s: %w", r.Source, r.Name, err)
		}
	}
	return tx.Commit()
}

func (s *Store) LatestDay(ctx context.Context, src string) (time.Time, bool, error) {
	query := "SELECT MAX(day) FROM metrics"
	var args []any
	if src != "" {
		query += " WHERE source = ?"
		args = append(args, src)
	}
	var dayStr sql.NullString
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&dayStr); err != nil {
		return time.Time{}, false, err
	}
	if !dayStr.Valid || dayStr.String == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, dayStr.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

func (s *Store) Query(ctx context.Context, q store.Query) ([]store.Record, error) {
	var (
		where []string
		args  []any
	)
	if q.Source != "" {
		where = append(where, "source = ?")
		args = append(args, q.Source)
	}
	if !q.From.IsZero() {
		where = append(where, "day >= ?")
		args = append(args, q.From.Format(time.RFC3339))
	}
	if !q.To.IsZero() {
		where = append(where, "day <= ?")
		args = append(args, q.To.Format(time.RFC3339))
	}
	sqlStr := "SELECT source, name, day, dimensions, value FROM metrics"
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY day, source, name"

	rows, err := s.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Record
	for rows.Next() {
		var (
			r       store.Record
			dayStr  string
			dimsStr string
		)
		if err := rows.Scan(&r.Source, &r.Name, &dayStr, &dimsStr, &r.Value); err != nil {
			return nil, err
		}
		if r.Day, err = time.Parse(time.RFC3339, dayStr); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dimsStr), &r.Dimensions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// canonicalDims serializes dimensions to a JSON object. encoding/json marshals
// map keys in sorted order, so identical maps always produce the same
// primary-key string regardless of Go map iteration order, and the result
// round-trips back into a map[string]string on read.
func canonicalDims(d map[string]string) (string, error) {
	if len(d) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
