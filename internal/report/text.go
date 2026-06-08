package report

import (
	"fmt"
	"io"
	"strings"
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
			fmt.Fprintf(w, "\n**%s**\n\n", s.Source)
		} else {
			fmt.Fprintf(w, "\n%s\n", strings.ToUpper(s.Source))
		}
		for _, t := range s.Totals {
			fmt.Fprintf(w, "  %-22s %12s\n", t.Label, fmtNum(t.Value))
		}
	}

	// Per-source charts as tables.
	for _, src := range doc.Sources {
		writeSection(w, md, src.Source)
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
	case "series":
		// Show the daily series (first period) as a compact recent tail.
		if len(c.Periods) == 0 {
			return
		}
		p := c.Periods[0]
		n := len(p.Labels)
		const tail = 14
		start := 0
		if n > tail {
			start = n - tail
		}
		writeKVTable(w, md, p.Period, "value", pairLabels(p.Labels[start:], p.Data[start:]))
	case "ordered", "breakdown":
		writeKVTable(w, md, "bucket", "count", labeledPairs(c.Bars))
	case "topn":
		writeKVTable(w, md, "name", "count", labeledPairs(c.Top))
	}
}

type kv struct {
	k string
	v float64
}

func labeledPairs(lvs []LabeledValue) []kv {
	out := make([]kv, len(lvs))
	for i, l := range lvs {
		out[i] = kv{l.Label, l.Value}
	}
	return out
}

func pairLabels(labels []string, data []float64) []kv {
	out := make([]kv, 0, len(labels))
	for i := range labels {
		var v float64
		if i < len(data) {
			v = data[i]
		}
		out = append(out, kv{labels[i], v})
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
