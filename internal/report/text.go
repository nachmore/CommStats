package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// RenderText writes a generic per-source text/markdown report built from the
// same Document the HTML view uses, so every source's metrics appear without
// source-specific rendering code. topN bounds top-N sections.
func RenderText(w io.Writer, doc Document, format Format) error {
	md := format == Markdown

	if md {
		fmt.Fprintf(w, "# CommStats Report\n\n_Generated %s · %s_\n", doc.GeneratedAt, doc.Span)
	} else {
		fmt.Fprintf(w, "CommStats Report  (%s)\n", doc.Span)
	}

	// Overview: per-source headline totals.
	writeSection(w, md, "Overview")
	for _, s := range doc.Overview.Sources {
		if md {
			fmt.Fprintf(w, "\n**%s**\n\n", s.Label)
		} else {
			fmt.Fprintf(w, "\n%s\n", strings.ToUpper(s.Label))
		}
		for _, t := range s.Totals {
			fmt.Fprintf(w, "  %-22s %12s\n", t.Label, fmtNum(t.Value))
		}
	}

	// Per-source charts as tables.
	for _, src := range doc.Sources {
		writeSection(w, md, src.Label)
		for _, c := range src.Charts {
			writeChart(w, md, c)
		}
	}
	return nil
}

func writeSection(w io.Writer, md bool, title string) {
	if md {
		fmt.Fprintf(w, "\n## %s\n", title)
	} else {
		bar := strings.Repeat("=", len(title)+8)
		fmt.Fprintf(w, "\n%s\n=== %s ===\n%s\n", bar, strings.ToUpper(title), bar)
	}
}

func writeChart(w io.Writer, md bool, c Chart) {
	if md {
		fmt.Fprintf(w, "\n### %s\n\n", c.Title)
	} else {
		fmt.Fprintf(w, "\n%s\n", c.Title)
	}

	switch c.Kind {
	case "series", "dual":
		// Daily totals, compact recent tail.
		rows := byDate(c.Points, c.Agg)
		const tail = 14
		if len(rows) > tail {
			rows = rows[len(rows)-tail:]
		}
		writeKVTable(w, md, "day", "value", rows)
	case "weekday":
		writeKVTable(w, md, "weekday", "avg", weekdayRows(c.Points))
	case "ordered":
		writeKVTable(w, md, "bucket", "count", sortedByKey(c.Points))
	case "topn":
		writeKVTable(w, md, "name", "count", topRows(c.Points, c.Labels, c.TopN))
	default: // breakdown / doughnut
		writeKVTable(w, md, "bucket", "count", sortedByValue(c.Points))
	}
}

type kv struct {
	k string
	v float64
}

// byDate sums (or counts-distinct) points per day, ordered chronologically.
func byDate(pts []DayPoint, agg string) []kv {
	if agg == "distinct" {
		sets := map[string]map[string]struct{}{}
		for _, p := range pts {
			if sets[p.Date] == nil {
				sets[p.Date] = map[string]struct{}{}
			}
			sets[p.Date][p.Key] = struct{}{}
		}
		var out []kv
		for _, d := range sortStr(keysOf(sets)) {
			out = append(out, kv{d, float64(len(sets[d]))})
		}
		return out
	}
	sums := map[string]float64{}
	for _, p := range pts {
		sums[p.Date] += p.Value
	}
	var out []kv
	for _, d := range sortStr(keysOfF(sums)) {
		out = append(out, kv{d, sums[d]})
	}
	return out
}

func sortedByValue(pts []DayPoint) []kv {
	sums := map[string]float64{}
	for _, p := range pts {
		sums[p.Key] += p.Value
	}
	out := make([]kv, 0, len(sums))
	for k, v := range sums {
		out = append(out, kv{k, v})
	}
	sortKVValueDesc(out)
	return out
}

func sortedByKey(pts []DayPoint) []kv {
	sums := map[string]float64{}
	for _, p := range pts {
		sums[p.Key] += p.Value
	}
	out := make([]kv, 0, len(sums))
	for _, k := range sortStr(keysOfF(sums)) {
		out = append(out, kv{k, sums[k]})
	}
	return out
}

func topRows(pts []DayPoint, labels map[string]string, n int) []kv {
	sums := map[string]float64{}
	for _, p := range pts {
		sums[p.Key] += p.Value
	}
	out := make([]kv, 0, len(sums))
	for id, v := range sums {
		label := id
		if labels != nil && labels[id] != "" {
			label = labels[id]
		}
		out = append(out, kv{label, v})
	}
	sortKVValueDesc(out)
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

func weekdayRows(pts []DayPoint) []kv {
	total := map[time.Weekday]float64{}
	days := map[time.Weekday]map[string]struct{}{}
	for _, p := range pts {
		t, err := time.Parse("2006-01-02", p.Date)
		if err != nil {
			continue
		}
		wd := t.Weekday()
		total[wd] += p.Value
		if days[wd] == nil {
			days[wd] = map[string]struct{}{}
		}
		days[wd][p.Date] = struct{}{}
	}
	var out []kv
	for _, wd := range weekdayOrder {
		avg := 0.0
		if n := len(days[wd]); n > 0 {
			avg = total[wd] / float64(n)
		}
		out = append(out, kv{wd.String()[:3], avg})
	}
	return out
}

func writeKVTable(w io.Writer, md bool, kHead, vHead string, rows []kv) {
	if len(rows) == 0 {
		if md {
			fmt.Fprintln(w, "_(no data)_")
		} else {
			fmt.Fprintln(w, "  (no data)")
		}
		return
	}
	if md {
		fmt.Fprintf(w, "| %s | %s |\n| --- | ---: |\n", kHead, vHead)
		for _, r := range rows {
			fmt.Fprintf(w, "| %s | %s |\n", r.k, fmtNum(r.v))
		}
		return
	}
	width := len(kHead)
	for _, r := range rows {
		if len(r.k) > width {
			width = len(r.k)
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %-*s  %10s\n", width, r.k, fmtNum(r.v))
	}
}

func fmtNum(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}

func keysOf(m map[string]map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfF(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortStr(s []string) []string { sort.Strings(s); return s }

func sortKVValueDesc(rows []kv) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].v != rows[j].v {
			return rows[i].v > rows[j].v
		}
		return rows[i].k < rows[j].k
	})
}
