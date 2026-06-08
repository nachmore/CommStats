// Package collect orchestrates a single collection pass: for each registered
// source, fetch metrics for the window and upsert them into the store, bucketed
// by local calendar day.
package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/nachmore/commstats/internal/source"
	"github.com/nachmore/commstats/internal/store"
)

// Result reports per-source outcomes from a pass.
type Result struct {
	Source string
	Count  int
	Err    error
}

// Run collects from every registered source over window and upserts results.
// One source failing does not abort the others; its error is returned in the
// corresponding Result. If only is non-empty, only the plugin with that
// registration name is collected.
func Run(ctx context.Context, st store.Store, window source.TimeWindow, only string) []Result {
	day := dayOf(window.End)
	srcs := source.Registered()
	results := make([]Result, 0, len(srcs))

	for _, s := range srcs {
		if only != "" && s.Name() != only {
			continue
		}
		res := Result{Source: s.Name()}
		metrics, err := s.Collect(ctx, window)
		if err != nil {
			res.Err = fmt.Errorf("collect: %w", err)
			results = append(results, res)
			continue
		}
		recs := make([]store.Record, len(metrics))
		for i, m := range metrics {
			recs[i] = store.Record{
				Source:     m.Source,
				Name:       m.Name,
				Day:        day,
				Value:      m.Value,
				Dimensions: m.Dimensions,
			}
		}
		if err := st.Upsert(ctx, recs); err != nil {
			res.Err = fmt.Errorf("upsert: %w", err)
		} else {
			res.Count = len(recs)
		}
		results = append(results, res)
	}
	return results
}

// dayOf truncates t to midnight in its own location.
func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
