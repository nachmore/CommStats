package report

import (
	"sort"
	"sync"

	"github.com/nachmore/commstats/internal/store"
)

// Reporter lets a source curate its own report instead of relying on the
// generic shape-based classifier. A source registers one via RegisterReporter
// from its init(); BuildDocument prefers it when present.
//
// Implementations build Charts from the source's records (using the exported
// chart-builder helpers in this package), and provide Headline totals for the
// overview tab — which the source must compute itself, since only it knows
// which of its metrics are canonical (e.g. that meetings{size=*} partitions
// meetings, so summing every meetings row would multi-count).
type Reporter interface {
	Charts(recs []store.Record, topN int) []Chart
	Headline(recs []store.Record) []LabeledValue
}

var (
	reporterMu sync.RWMutex
	reporters  = map[string]Reporter{}
)

// RegisterReporter associates a curated Reporter with a source name.
func RegisterReporter(source string, r Reporter) {
	reporterMu.Lock()
	defer reporterMu.Unlock()
	reporters[source] = r
}

func reporterFor(source string) (Reporter, bool) {
	reporterMu.RLock()
	defer reporterMu.RUnlock()
	r, ok := reporters[source]
	return r, ok
}

// recordsBySource groups records by source, returning the groups and a sorted
// list of source names.
func recordsBySource(recs []store.Record) (map[string][]store.Record, []string) {
	by := map[string][]store.Record{}
	for _, r := range recs {
		by[r.Source] = append(by[r.Source], r)
	}
	names := make([]string, 0, len(by))
	for s := range by {
		names = append(names, s)
	}
	sort.Strings(names)
	return by, names
}
