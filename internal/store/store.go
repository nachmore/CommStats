// Package store persists collected metrics behind an interface so the SQLite
// backend can later be swapped for a server/cloud store without touching the
// collect or report layers.
package store

import (
	"context"
	"time"
)

// Record is a stored metric: a Metric pinned to the local calendar day it was
// bucketed into. Day is midnight-local of the collection window's end.
type Record struct {
	Source     string
	Name       string
	Day        time.Time
	Value      float64
	Dimensions map[string]string
}

// Query selects records for reporting.
type Query struct {
	Source string    // empty = all sources
	From   time.Time // inclusive day
	To     time.Time // inclusive day
}

// Store persists and retrieves metrics.
//
// Upsert must be idempotent on the natural key (source, name, day, dimensions):
// re-running a collection for the same day replaces that day's value rather
// than adding to it, since sources report absolute counts. This is what lets
// the tool run multiple times a day and show live-updating totals.
type Store interface {
	Upsert(ctx context.Context, recs []Record) error
	// ReplaceDay atomically deletes all existing rows for the given day and
	// metric-sources, then inserts recs. Unlike Upsert it removes stale rows a
	// re-collection no longer produces (e.g. a metric/dimension that dropped
	// out after a config change), so the day reflects exactly the new recs.
	ReplaceDay(ctx context.Context, day time.Time, sources []string, recs []Record) error
	Query(ctx context.Context, q Query) ([]Record, error)
	// LatestDay returns the most recent day that has any records for src (empty
	// src = any source). ok is false when the store has no matching data.
	LatestDay(ctx context.Context, src string) (day time.Time, ok bool, err error)
	Close() error
}
