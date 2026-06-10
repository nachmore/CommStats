package report

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
)

// RenderHTML writes a self-contained interactive HTML report: a cross-source
// Overview tab plus one tab per source, charts rendered by Chart.js from the
// embedded Document. The data is also embedded so the page is fully offline
// except for the Chart.js CDN script.
func RenderHTML(w io.Writer, doc Document) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal report data: %w", err)
	}
	return htmlTemplate.Execute(w, struct {
		Doc  Document
		JSON template.JS
	}{Doc: doc, JSON: template.JS(data)})
}

var htmlTemplate = template.Must(template.New("report").Parse(htmlSource))

const htmlSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>CommStats Report</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.1/dist/chart.umd.min.js"></script>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif; margin: 0;
         background: #0f1117; color: #e6e6e6; }
  header { padding: 20px 24px 0; }
  h1 { margin: 0 0 2px; font-size: 22px; }
  .muted { color: #8a8f98; font-size: 13px; }
  .tabs { display: flex; gap: 4px; padding: 12px 24px 0; border-bottom: 1px solid #2a2e37; flex-wrap: wrap; }
  .tab { padding: 8px 16px; cursor: pointer; border: 1px solid transparent; border-bottom: none;
         border-radius: 8px 8px 0 0; color: #8a8f98; font-size: 14px; }
  .tab.active { background: #161922; border-color: #2a2e37; color: #e6e6e6; }
  .panel { display: none; padding: 20px 24px; }
  .panel.active { display: block; }
  .grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 20px; }
  .card { background: #161922; border: 1px solid #2a2e37; border-radius: 10px; padding: 16px; }
  .card.wide { grid-column: 1 / -1; }
  .card h3 { margin: 0 0 12px; font-size: 14px; font-weight: 600; color: #c9cdd6; }
  canvas { max-height: 320px; }
  select { background: #0f1117; color: #e6e6e6; border: 1px solid #2a2e37; border-radius: 6px;
           padding: 4px 8px; font-size: 13px; }
  .headlines { display: flex; flex-wrap: wrap; gap: 16px; margin-bottom: 28px; }
  .headline { background: #161922; border: 1px solid #2a2e37; border-radius: 10px; padding: 14px 18px; min-width: 180px; }
  .headline h4 { margin: 0 0 8px; font-size: 13px; text-transform: uppercase; color: #8a8f98; }
  .headline .row { display: flex; justify-content: space-between; font-size: 13px; padding: 2px 0; }
  .headline .row .n { font-variant-numeric: tabular-nums; color: #c9cdd6; }
  .topbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px;
            padding: 20px 24px 0; flex-wrap: wrap; }
  .globals { display: flex; gap: 14px; align-items: center; }
  .globals label { font-size: 12px; color: #8a8f98; display: flex; gap: 6px; align-items: center; }
</style>
</head>
<body>
  <div class="topbar">
    <header style="padding:0">
      <h1>CommStats Report</h1>
      <div class="muted">Generated {{.Doc.GeneratedAt}} · {{.Doc.Span}}</div>
    </header>
    <div class="globals">
      <label>Period
        <select id="g-window">
          <option value="7">Last 7 days</option>
          <option value="30">Last 30 days</option>
          <option value="90" selected>Last 90 days</option>
          <option value="0">All time</option>
        </select>
      </label>
      <label>Aggregate by
        <select id="g-agg">
          <option value="day" selected>Day</option>
          <option value="week">Week</option>
          <option value="month">Month</option>
        </select>
      </label>
    </div>
  </div>

  <div class="tabs" id="tabs"></div>
  <div id="panels"></div>

<script>
const DATA = {{.JSON}};
const PALETTE = ["#5b9bd5","#e0a458","#70ad47","#c0504d","#9b59b6","#4bc0c0","#f06292","#a0a0a0","#8dd3c7","#fb8072"];
const color = i => PALETTE[i % PALETTE.length];

// Global controls drive every chart: window (days back, 0=all) + aggregation.
const STATE = { windowDays: 90, agg: "day" };
// Each rendered chart registers a redraw closure here.
const REDRAW = [];

function el(tag, cls, html) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (html !== undefined) e.innerHTML = html;
  return e;
}
function makeCanvas(card) { const c = document.createElement("canvas"); card.appendChild(c); return c; }

// --- windowing + aggregation over daily points ---

// The newest date present across all data, as the window's anchor.
const MAX_DATE = (() => {
  let m = "";
  (DATA.sources || []).forEach(s => (s.charts || []).forEach(c => {
    (c.points || []).forEach(p => { if (p.d > m) m = p.d; });
    if (c.right) (c.right.points || []).forEach(p => { if (p.d > m) m = p.d; });
  }));
  return m;
})();

function cutoff() {
  if (!STATE.windowDays || !MAX_DATE) return "0000-00-00";
  const d = new Date(MAX_DATE + "T00:00:00");
  d.setDate(d.getDate() - (STATE.windowDays - 1));
  return d.toISOString().slice(0, 10);
}

function inWindow(points) {
  const c = cutoff();
  return (points || []).filter(p => p.d >= c);
}

// bucketKey maps a YYYY-MM-DD date to the label for the current aggregation.
function bucketKey(date) {
  if (STATE.agg === "day") return date.slice(5); // MM-DD
  const d = new Date(date + "T00:00:00");
  if (STATE.agg === "week") {
    const mon = new Date(d); mon.setDate(d.getDate() - ((d.getDay() + 6) % 7));
    return "wk " + mon.toISOString().slice(5, 10);
  }
  return date.slice(0, 7); // YYYY-MM
}

// aggregateSeries buckets windowed points by the current granularity, summing
// or counting-distinct, returning {labels, data} in chronological order.
function aggregateSeries(points, agg) {
  const pts = inWindow(points);
  const order = [];
  const map = {};
  pts.forEach(p => {
    const k = bucketKey(p.d);
    if (!(k in map)) { map[k] = agg === "distinct" ? new Set() : 0; order.push([k, sortKeyForBucket(p.d)]); }
    if (agg === "distinct") map[k].add(p.k); else map[k] += p.v;
  });
  order.sort((a, b) => a[1] < b[1] ? -1 : 1);
  const seen = new Set(); const labels = []; const data = [];
  order.forEach(([k]) => {
    if (seen.has(k)) return; seen.add(k);
    labels.push(k);
    data.push(agg === "distinct" ? map[k].size : map[k]);
  });
  return { labels, data };
}
function sortKeyForBucket(date) {
  if (STATE.agg === "day") return date;
  const d = new Date(date + "T00:00:00");
  if (STATE.agg === "week") { const m = new Date(d); m.setDate(d.getDate() - ((d.getDay()+6)%7)); return m.toISOString().slice(0,10); }
  return date.slice(0,7);
}

// sumByKey totals windowed points by their categorical key.
function sumByKey(points) {
  const m = {}; inWindow(points).forEach(p => { m[p.k] = (m[p.k]||0) + p.v; }); return m;
}
// weekdayAvg averages windowed point values per weekday (Mon..Sun).
function weekdayAvg(points) {
  const tot = [0,0,0,0,0,0,0], days = [new Set(),new Set(),new Set(),new Set(),new Set(),new Set(),new Set()];
  inWindow(points).forEach(p => {
    const wd = (new Date(p.d+"T00:00:00").getDay() + 6) % 7; // 0=Mon
    tot[wd] += p.v; days[wd].add(p.d);
  });
  const labels = ["Mon","Tue","Wed","Thu","Fri","Sat","Sun"];
  return { labels, data: tot.map((t,i) => days[i].size ? t/days[i].size : 0) };
}

// --- chart rendering (registers a redraw on STATE change) ---

function register(chartObj, recompute) {
  REDRAW.push(() => { const d = recompute(); chartObj.data = d; chartObj.update(); });
}

function lineChart(card, chart) {
  const cv = makeCanvas(card);
  const recompute = () => {
    const s = aggregateSeries(chart.points, chart.agg);
    return { labels: s.labels, datasets: [{ label: chart.title, data: s.data,
             borderColor: color(0), backgroundColor: color(0), tension: 0.25 }] };
  };
  const ch = new Chart(cv, { type: "line", data: recompute(),
    options: { responsive: true, plugins: { legend: { display: false } } } });
  register(ch, recompute);
}

function dualChart(card, chart) {
  const cv = makeCanvas(card);
  const ln = (chart.labels && chart.labels.left) || "left";
  const rn = (chart.labels && chart.labels.right) || "right";
  const recompute = () => {
    const L = aggregateSeries(chart.points, chart.agg);
    const R = aggregateSeries(chart.right.points, chart.right.agg);
    return { labels: L.labels, datasets: [
      { label: ln, data: L.data, borderColor: color(0), backgroundColor: color(0), tension: 0.25, yAxisID: "y" },
      { label: rn, data: R.data, borderColor: color(1), backgroundColor: color(1), tension: 0.25, yAxisID: "y1" } ] };
  };
  const ch = new Chart(cv, { type: "line", data: recompute(),
    options: { responsive: true, interaction: { mode: "index", intersect: false },
      scales: { y: { position: "left", title: { display: true, text: ln } },
                y1: { position: "right", title: { display: true, text: rn }, grid: { drawOnChartArea: false } } } } });
  register(ch, recompute);
}

function barChart(card, chart, kind) {
  const cv = makeCanvas(card);
  const recompute = () => {
    const m = sumByKey(chart.points);
    let entries = Object.entries(m);
    if (kind === "ordered") entries.sort((a,b) => a[0] < b[0] ? -1 : 1);
    else entries.sort((a,b) => b[1] - a[1]);
    if (kind === "topn" && chart.topn) entries = entries.slice(0, chart.topn);
    const labels = entries.map(([k]) => (chart.labels && chart.labels[k]) || k);
    const data = entries.map(([,v]) => v);
    return { labels, datasets: [{ label: chart.title, data,
             backgroundColor: data.map((_, i) => color(i)) }] };
  };
  const horizontal = (kind === "topn" || kind === "breakdown");
  const ch = new Chart(cv, { type: "bar", data: recompute(),
    options: { indexAxis: horizontal ? "y" : "x", responsive: true, plugins: { legend: { display: false } } } });
  register(ch, recompute);
}

function weekdayChart(card, chart) {
  const cv = makeCanvas(card);
  const recompute = () => {
    const w = weekdayAvg(chart.points);
    return { labels: w.labels, datasets: [{ label: chart.title, data: w.data,
             backgroundColor: w.labels.map((_, i) => color(i)) }] };
  };
  const ch = new Chart(cv, { type: "bar", data: recompute(),
    options: { responsive: true, plugins: { legend: { display: false } } } });
  register(ch, recompute);
}

function doughnutChart(card, chart) {
  const cv = makeCanvas(card);
  const recompute = () => {
    const m = sumByKey(chart.points);
    const entries = Object.entries(m).sort((a,b) => b[1] - a[1]);
    return { labels: entries.map(([k]) => k),
             datasets: [{ data: entries.map(([,v]) => v), backgroundColor: entries.map((_, i) => color(i)) }] };
  };
  const ch = new Chart(cv, { type: "doughnut", data: recompute(),
    options: { responsive: true, plugins: { legend: { position: "bottom" } } } });
  register(ch, recompute);
}

function renderChart(grid, chart) {
  const card = el("div", "card");
  if (chart.kind === "topn" || chart.kind === "series" || chart.kind === "dual") card.classList.add("wide");
  card.appendChild(el("h3", null, chart.title));
  switch (chart.kind) {
    case "series":   lineChart(card, chart); break;
    case "dual":     dualChart(card, chart); break;
    case "ordered":  barChart(card, chart, "ordered"); break;
    case "topn":     barChart(card, chart, "topn"); break;
    case "weekday":  weekdayChart(card, chart); break;
    case "doughnut": doughnutChart(card, chart); break;
    default:         barChart(card, chart, "breakdown"); break;
  }
  grid.appendChild(card);
}

// --- overview ---

function reduceStat(stat) {
  const pts = inWindow(stat.points);
  if (stat.reduce === "distinct") {
    const set = new Set(); pts.forEach(p => set.add(p.k)); return set.size;
  }
  if (stat.reduce === "afterhours") {
    let after = 0, total = 0;
    pts.forEach(p => {
      const h = parseInt(p.k, 10); total += p.v;
      const wd = new Date(p.d + "T00:00:00").getDay(); // 0=Sun..6=Sat
      if (wd === 0 || wd === 6 || h < 8 || h >= 18) after += p.v;
    });
    return total ? after / total * 100 : 0;
  }
  let sum = 0; pts.forEach(p => sum += p.v); return sum; // "sum"
}

function fmtStat(stat, v) {
  return stat.pct ? Math.round(v) + "%" : Math.round(v).toLocaleString();
}

function renderOverview(panel) {
  const ov = DATA.overview || {};
  const hl = el("div", "headlines");
  (ov.sources || []).forEach(s => {
    const card = el("div", "headline");
    card.appendChild(el("h4", null, s.label || s.source));
    const addRow = (label, compute) => {
      const row = el("div", "row");
      row.appendChild(el("span", null, label));
      const n = el("span", "n");
      row.appendChild(n);
      card.appendChild(row);
      REDRAW.push(() => { n.textContent = compute(); });
      n.textContent = compute();
    };
    (s.stats || []).forEach(stat => addRow(stat.label, () => fmtStat(stat, reduceStat(stat))));
    // Estimated time + a daily average over active days in the window.
    const est = EST_TIME[s.source];
    if (est) {
      addRow("est. time", () => {
        const mins = inWindow(est).reduce((a,p)=>a+p.v,0);
        return mins >= 60 ? (mins/60).toFixed(1) + " h" : Math.round(mins) + " m";
      });
      addRow("est. time / active day", () => {
        const pts = inWindow(est);
        const days = new Set(); let mins = 0;
        pts.forEach(p => { if (p.v > 0) { days.add(p.d); mins += p.v; } });
        return days.size ? Math.round(mins/days.size) + " m" : "0 m";
      });
    }
    hl.appendChild(card);
  });
  panel.appendChild(hl);

  // "Where does my time go" — estimated time across mediums in a common unit
  // (minutes). Meetings are actual; messages/emails use rough per-item
  // heuristics, so the figures are estimates.
  const note = el("div", "muted");
  note.style.margin = "0 0 14px";
  note.textContent = "Time is estimated: meetings use actual minutes; Slack ≈ 0.5 min/message, email ≈ 1.3 min/message (read+sent). Rough heuristics.";
  panel.appendChild(note);

  const grid = el("div", "grid");
  timeSpentDoughnut(grid);
  timeTrendStacked(grid);
  combinedActivity(grid, "Activity by hour of day (% of each source's peak)", "hour");
  panel.appendChild(grid);
}

// timeSpentDoughnut shows estimated hours per source over the window, with the
// total in the title — the headline "where does my time go" answer.
function timeSpentDoughnut(grid) {
  const card = el("div", "card");
  const h = el("h3", null, "Estimated time spent");
  card.appendChild(h);
  const cv = makeCanvas(card);
  const srcs = Object.keys(EST_TIME);
  const recompute = () => {
    const hrs = srcs.map(s => inWindow(EST_TIME[s]).reduce((a,p)=>a+p.v,0) / 60);
    const total = hrs.reduce((a,b)=>a+b,0);
    h.textContent = "Estimated time spent — " + Math.round(total).toLocaleString() + " h total";
    return { labels: srcs.map(labelFor), datasets: [{ data: hrs, backgroundColor: srcs.map((_,i)=>color(i)) }] };
  };
  const ch = new Chart(cv, { type: "doughnut", data: recompute(),
    options: { responsive: true, plugins: { legend: { position: "bottom" },
      tooltip: { callbacks: { label: c => c.label + ": " + Math.round(c.parsed).toLocaleString() + " h" } } } } });
  register(ch, recompute);
  grid.appendChild(card);
}

// timeTrendStacked shows estimated hours per source over time, stacked, at the
// global aggregation — total comms load and its composition trending.
function timeTrendStacked(grid) {
  const card = el("div", "card");
  card.appendChild(el("h3", null, "Estimated time over period (stacked)"));
  const cv = makeCanvas(card);
  const srcs = Object.keys(EST_TIME);
  const recompute = () => {
    // Union of bucket labels across sources, chronological.
    const perSrc = srcs.map(s => aggregateSeries(EST_TIME[s], "sum"));
    const labelSet = [];
    const seen = new Set();
    perSrc.forEach(a => a.labels.forEach(l => { if (!seen.has(l)) { seen.add(l); labelSet.push(l); } }));
    const datasets = srcs.map((s, i) => {
      const a = perSrc[i];
      const m = {}; a.labels.forEach((l, j) => m[l] = a.data[j] / 60);
      return { label: labelFor(s), data: labelSet.map(l => m[l] || 0), backgroundColor: color(i) };
    });
    return { labels: labelSet, datasets };
  };
  const ch = new Chart(cv, { type: "bar", data: recompute(),
    options: { responsive: true, scales: { x: { stacked: true }, y: { stacked: true, title: { display: true, text: "estimated hours" } } } } });
  register(ch, recompute);
  grid.appendChild(card);
}

function labelFor(src) {
  const s = (DATA.sources || []).find(x => x.source === src);
  return (s && s.label) || src;
}

// combinedActivity builds a normalized grouped-bar chart across sources, from
// each source's weekday/hour metric points, recomputed on window change.
function combinedActivity(grid, title, mode) {
  const card = el("div", "card wide");
  card.appendChild(el("h3", null, title));
  const cv = makeCanvas(card);
  const labels = mode === "weekday"
    ? ["Mon","Tue","Wed","Thu","Fri","Sat","Sun"]
    : Array.from({length:24}, (_,i) => String(i).padStart(2,"0"));
  const recompute = () => {
    const datasets = [];
    (DATA.sources || []).forEach((s, si) => {
      const pts = (OVERVIEW_ACTIVITY[s.source] || {})[mode];
      if (!pts) return;
      let arr;
      if (mode === "weekday") {
        arr = weekdayAvg(pts).data;
      } else {
        const sums = new Array(24).fill(0);
        inWindow(pts).forEach(p => { const h = parseInt(p.k,10); if (h>=0&&h<24) sums[h]+=p.v; });
        arr = sums;
      }
      const max = Math.max(...arr, 0);
      const norm = max ? arr.map(v => v/max*100) : arr;
      datasets.push({ label: s.label || s.source, data: norm, backgroundColor: color(si) });
    });
    return { labels, datasets };
  };
  const ch = new Chart(cv, { type: "bar", data: recompute(),
    options: { responsive: true, scales: { y: { title: { display: true, text: "% of own peak" }, max: 100 } } } });
  register(ch, recompute);
  grid.appendChild(card);
}

// OVERVIEW_ACTIVITY: per-source daily points for weekday (primary metric) and
// hour (hour metric), provided by the document for client-side windowing.
const OVERVIEW_ACTIVITY = DATA.overview && DATA.overview.activity || {};
// EST_TIME: per-source daily estimated-minutes points for the time-spent views.
const EST_TIME = DATA.overview && DATA.overview.est_time || {};

// --- tabs ---

function addTab(name, render) {
  const tabs = document.getElementById("tabs");
  const panels = document.getElementById("panels");
  const tab = el("div", "tab", name);
  const panel = el("div", "panel");
  tabs.appendChild(tab);
  panels.appendChild(panel);
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach(t => t.classList.remove("active"));
    document.querySelectorAll(".panel").forEach(p => p.classList.remove("active"));
    tab.classList.add("active"); panel.classList.add("active");
  });
  render(panel);
  return { tab, panel };
}

const first = addTab("Overview", renderOverview);
(DATA.sources || []).forEach(s => {
  addTab(s.label || s.source, panel => {
    if (window.Chart) {
      const grid = el("div", "grid");
      panel.appendChild(grid);
      (s.charts || []).forEach(c => renderChart(grid, c));
    }
    renderTables(panel, s);
  });
});
first.tab.classList.add("active");
first.panel.classList.add("active");

// Wire global controls to redraw every registered chart.
function applyGlobals() {
  STATE.windowDays = parseInt(document.getElementById("g-window").value, 10) || 0;
  STATE.agg = document.getElementById("g-agg").value;
  REDRAW.forEach(fn => fn());
}
document.getElementById("g-window").addEventListener("change", applyGlobals);
document.getElementById("g-agg").addEventListener("change", applyGlobals);

// --- raw data tables (collapsed; also fallback when charts don't load) ---

function renderTables(panel, s) {
  const det = el("details");
  if (!window.Chart) det.setAttribute("open", "");
  det.appendChild(el("summary", null, "Raw data tables"));
  (s.charts || []).forEach(c => {
    det.appendChild(el("h3", null, c.title));
    let rows;
    if (c.kind === "series" || c.kind === "dual") {
      const agg = aggregateSeries(c.points, c.agg);
      rows = agg.labels.map((l, i) => [l, agg.data[i]]);
    } else if (c.kind === "weekday") {
      const w = weekdayAvg(c.points);
      rows = w.labels.map((l, i) => [l, w.data[i]]);
    } else {
      const m = sumByKey(c.points);
      let entries = Object.entries(m);
      if (c.kind === "ordered") entries.sort((a,b)=>a[0]<b[0]?-1:1); else entries.sort((a,b)=>b[1]-a[1]);
      if (c.kind === "topn" && c.topn) entries = entries.slice(0, c.topn);
      rows = entries.map(([k,v]) => [(c.labels && c.labels[k]) || k, v]);
    }
    det.appendChild(kvTable(c.kind === "topn" ? "name" : "bucket", "value", rows));
  });
  panel.appendChild(det);
}

function kvTable(kHead, vHead, rows) {
  const t = el("table");
  t.innerHTML = "<thead><tr><th>"+kHead+"</th><th>"+vHead+"</th></tr></thead>";
  const tb = el("tbody");
  rows.forEach(r => {
    const tr = el("tr");
    tr.appendChild(el("td", null, String(r[0])));
    tr.appendChild(el("td", null, Math.round(r[1]).toLocaleString()));
    tb.appendChild(tr);
  });
  t.appendChild(tb);
  return t;
}
</script>
</body>
</html>
`
