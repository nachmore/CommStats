// Command commstats collects communication-usage stats and renders reports.
//
// Usage:
//
//	commstats collect [--days N | --from YYYY-MM-DD [--to YYYY-MM-DD]]
//	commstats report  [--by day|week|month|year] [--days N] [--format terminal|markdown] [--source NAME]
//	commstats login   <source>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nachmore/commstats/internal/collect"
	"github.com/nachmore/commstats/internal/config"
	"github.com/nachmore/commstats/internal/platform"
	"github.com/nachmore/commstats/internal/report"
	"github.com/nachmore/commstats/internal/source"
	"github.com/nachmore/commstats/internal/store"
	"github.com/nachmore/commstats/internal/store/sqlite"

	// Comm sources self-register via blank import. Add new sources here.
	_ "github.com/nachmore/commstats/internal/source/slack"
)

// version is the build version, overridden at release time via
// -ldflags "-X main.version=...". "dev" for local/un-stamped builds.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "collect":
		err = runCollect(source.WithInteractive(ctx), os.Args[2:])
	case "report":
		err = runReport(ctx, os.Args[2:])
	case "login":
		err = runLogin(source.WithInteractive(ctx), os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("commstats", version)
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `commstats - communication usage stats

Commands:
  collect   Collect stats from all sources into the local store
  report    Render a report from stored stats
  login     Authenticate a source interactively (e.g. "login slack")

Run "commstats <command> -h" for command flags.
`)
}

func runLogin(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: commstats login <source>")
	}
	name := args[0]
	s, ok := source.Lookup(name)
	if !ok {
		return fmt.Errorf("unknown source %q", name)
	}
	li, ok := s.(interface {
		Login(context.Context) error
	})
	if !ok {
		return fmt.Errorf("source %q does not support login", name)
	}
	if err := li.Login(ctx); err != nil {
		return err
	}
	fmt.Printf("%s: login saved\n", name)
	return nil
}

func runCollect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	days := fs.Int("days", 0, "collect the most recent N days (0 = gap-fill from last run to today)")
	from := fs.String("from", "", "backfill start date YYYY-MM-DD (inclusive); overrides --days")
	to := fs.String("to", "", "backfill end date YYYY-MM-DD (inclusive); defaults to today")
	fs.Parse(args)

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	// Gap-fill default: when neither --from nor --days is given, collect from
	// the day after the last stored day through today, so a run after several
	// idle days fills the gap. Empty store falls back to today only.
	var lastDay time.Time
	if *from == "" && *days == 0 {
		if d, ok, err := st.LatestDay(ctx, ""); err != nil {
			return err
		} else if ok {
			lastDay = d
		}
	}

	dayList, err := collectionDays(*days, *from, *to, lastDay)
	if err != nil {
		return err
	}
	if len(dayList) == 0 {
		fmt.Println("already up to date")
		return nil
	}

	var failed bool
	// One window per calendar day: each day is bucketed and upserted
	// independently, so backfills are resumable and re-runs don't double-count.
	for i, dayEnd := range dayList {
		window := source.TimeWindow{Start: dayEnd.AddDate(0, 0, -1), End: dayEnd}
		label := dayEnd.Format("2006-01-02")

		for _, r := range collect.Run(ctx, st, window) {
			if r.Err != nil {
				failed = true
				fmt.Printf("  [%d/%d] %s  %-12s FAILED: %v\n", i+1, len(dayList), label, r.Source, r.Err)
				continue
			}
			fmt.Printf("  [%d/%d] %s  %-12s %d metrics\n", i+1, len(dayList), label, r.Source, r.Count)
		}
	}
	if failed {
		return fmt.Errorf("one or more sources failed")
	}
	return nil
}

// collectionDays returns the ordered (oldest-first) list of day-end timestamps
// to collect. Precedence: explicit --from range; else most recent --days days;
// else gap-fill from lastDay+1 through today (re-collecting lastDay itself so a
// partial final day is completed). Order is oldest-first so progress output
// reads naturally for backfills.
func collectionDays(days int, from, to string, lastDay time.Time) ([]time.Time, error) {
	now := time.Now()
	if from != "" {
		start, err := time.ParseInLocation("2006-01-02", from, now.Location())
		if err != nil {
			return nil, fmt.Errorf("invalid --from: %w", err)
		}
		end := now
		if to != "" {
			if end, err = time.ParseInLocation("2006-01-02", to, now.Location()); err != nil {
				return nil, fmt.Errorf("invalid --to: %w", err)
			}
		}
		if end.Before(start) {
			return nil, fmt.Errorf("--to (%s) is before --from (%s)", to, from)
		}
		return daysInRange(dayOf(start), dayOf(end)), nil
	}

	// Gap-fill: re-collect from the last stored day (to complete a partial day)
	// through today.
	if days == 0 && !lastDay.IsZero() {
		return daysInRange(dayOf(lastDay), dayOf(now)), nil
	}

	n := days
	if n < 1 {
		n = 1 // empty store, no flags: just today
	}
	out := make([]time.Time, 0, n)
	for d := n - 1; d >= 0; d-- {
		out = append(out, endOfDay(now.AddDate(0, 0, -d)))
	}
	return out, nil
}

// daysInRange returns end-of-day timestamps for each day in [start, end]
// inclusive, oldest-first. start and end should be day-truncated.
func daysInRange(start, end time.Time) []time.Time {
	var out []time.Time
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		out = append(out, endOfDay(d))
	}
	return out
}

// ignoreSet excludes configured channels/DMs from reports. Loaded once per
// report invocation and applied to every store query via queryRecords.
var ignoreSet *config.IgnoreSet

func runReport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	by := fs.String("by", "", "granularity: day|week|month|year (default: all-periods overview)")
	days := fs.Int("days", 0, "lookback in days for --by (0 = period-appropriate default)")
	format := fs.String("format", "terminal", "output format: terminal|markdown|html")
	src := fs.String("source", "", "limit to a single source")
	top := fs.Int("top", 15, "number of channels in the top-channels section")
	out := fs.String("out", "", "output file for --format html (default: ConfigDir/report-DATE.html)")
	fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ignoreSet = config.NewIgnoreSet(cfg.Ignore)

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	fmtv := report.Format(*format)

	// Warn about ignore entries that matched nothing (likely typos) once the
	// report has run and every record has been checked against the set.
	defer warnUnmatchedIgnores()

	// HTML is an interactive, charted view over the full retained range.
	if fmtv == report.HTML {
		return runHTMLReport(ctx, st, *src, *top, *out)
	}

	// Default view: stacked sections for every period granularity + top channels.
	if *by == "" {
		return renderOverview(ctx, st, *src, fmtv, *top)
	}

	period, lookback, err := periodSpec(*by)
	if err != nil {
		return err
	}
	if *days > 0 {
		lookback = *days
	}
	return renderSection(ctx, st, *src, fmtv, period, lookback)
}

// warnUnmatchedIgnores prints a stderr warning for each configured ignore entry
// that never matched any record, so a typo'd entry is surfaced rather than
// silently doing nothing.
func warnUnmatchedIgnores() {
	for _, u := range ignoreSet.Unmatched() {
		fmt.Fprintf(os.Stderr, "warning: ignore entry %q matched no channels or DMs\n", u)
	}
}

// queryRecords runs a store query and drops any records matching the configured
// ignore set, so ignored channels/DMs are excluded from every report view.
func queryRecords(ctx context.Context, st store.Store, q store.Query) ([]store.Record, error) {
	recs, err := st.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return ignoreSet.Filter(recs), nil
}

// runHTMLReport builds the interactive HTML report over all stored data, writes
// it to a file, and opens it in the default browser.
func runHTMLReport(ctx context.Context, st store.Store, src string, top int, out string) error {
	now := time.Now()
	recs, err := queryRecords(ctx, st, store.Query{Source: src})
	if err != nil {
		return err
	}
	report.DetectSelf(recs)

	doc := report.BuildDocument(recs, now.Format("2006-01-02 15:04"), top)

	if out == "" {
		dir := platform.Current().Paths().ConfigDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}
		out = filepath.Join(dir, "report-"+now.Format("2006-01-02")+".html")
	}

	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create report file: %w", err)
	}
	if err := report.RenderHTML(f, doc); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	fmt.Printf("wrote %s\n", out)
	if err := platform.Current().Browser().Open(out); err != nil {
		// Opening is best-effort; the file is already written.
		fmt.Printf("(could not auto-open: %v)\n", err)
	}
	return nil
}

// periodSpec maps a --by value to its report.Period and a default lookback in
// days that yields a useful number of columns for that granularity.
func periodSpec(by string) (report.Period, int, error) {
	switch by {
	case "day":
		return report.Day, 14, nil
	case "week":
		return report.Week, 56, nil // ~8 weeks
	case "month":
		return report.Month, 365, nil // ~12 months
	case "year":
		return report.Year, 365 * 3, nil
	default:
		return 0, 0, fmt.Errorf("invalid --by %q (want day|week|month|year)", by)
	}
}

// renderSection renders a single granularity over the last `days` days.
func renderSection(ctx context.Context, st store.Store, src string, format report.Format, period report.Period, days int) error {
	now := time.Now()
	recs, err := queryRecords(ctx, st, store.Query{
		Source: src,
		From:   dayOf(now.AddDate(0, 0, -(days - 1))),
		To:     dayOf(now),
	})
	if err != nil {
		return err
	}
	return report.RenderSummary(os.Stdout, recs, format, period)
}

// renderOverview stacks daily, weekly, monthly, and yearly summary sections
// plus a top-channels section into one view — the default report.
func renderOverview(ctx context.Context, st store.Store, src string, format report.Format, top int) error {
	if format == report.Markdown {
		fmt.Fprintf(os.Stdout, "# CommStats Report\n\n_Generated %s_\n", time.Now().Format("2006-01-02 15:04"))
	}
	sections := []struct {
		period report.Period
		days   int
	}{
		{report.Day, 14},
		{report.Week, 56},
		{report.Month, 365},
		{report.Year, 365 * 3},
	}
	for _, s := range sections {
		writeHeader(format, s.period.Title())
		if err := renderSection(ctx, st, src, format, s.period, s.days); err != nil {
			return err
		}
	}

	// Full-range sections (weekday, top conversations) span all stored data.
	recs, err := queryRecords(ctx, st, store.Query{Source: src})
	if err != nil {
		return err
	}
	report.DetectSelf(recs)
	span := spanLabel(recs)

	writeHeader(format, "By Day of Week ("+span+")")
	if err := report.RenderWeekday(os.Stdout, recs, format); err != nil {
		return err
	}

	writeHeader(format, fmt.Sprintf("Top %d Conversations (%s)", top, span))
	return report.RenderTopChannels(os.Stdout, recs, format, top)
}

// spanLabel summarizes the date range covered by recs, e.g. "2026-03-10 to
// 2026-06-08" or "no data".
func spanLabel(recs []store.Record) string {
	var lo, hi time.Time
	for _, r := range recs {
		if lo.IsZero() || r.Day.Before(lo) {
			lo = r.Day
		}
		if hi.IsZero() || r.Day.After(hi) {
			hi = r.Day
		}
	}
	if lo.IsZero() {
		return "no data"
	}
	return lo.Format("2006-01-02") + " to " + hi.Format("2006-01-02")
}

func writeHeader(format report.Format, header string) {
	if format == report.Markdown {
		fmt.Fprintf(os.Stdout, "\n## %s\n", header)
	} else {
		fmt.Fprintf(os.Stdout, "\n========== %s ==========\n", strings.ToUpper(header))
	}
}

func openStore(ctx context.Context) (store.Store, error) {
	dir := platform.Current().Paths().DataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return sqlite.Open(ctx, filepath.Join(dir, "commstats.db"))
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// endOfDay returns the last instant of t's calendar day. Used as a window End
// so both the day bucket (dayOf(End)) and the Slack `on:` date resolve to that
// calendar day regardless of the time of collection.
func endOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
}
