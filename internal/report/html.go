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
  .controls { margin-bottom: 10px; }
  select { background: #0f1117; color: #e6e6e6; border: 1px solid #2a2e37; border-radius: 6px;
           padding: 4px 8px; font-size: 13px; }
  .headlines { display: flex; flex-wrap: wrap; gap: 16px; }
  .headline { background: #161922; border: 1px solid #2a2e37; border-radius: 10px; padding: 14px 18px; min-width: 180px; }
  .headline h4 { margin: 0 0 8px; font-size: 13px; text-transform: uppercase; color: #8a8f98; }
  .headline .row { display: flex; justify-content: space-between; font-size: 13px; padding: 2px 0; }
  .headline .row .n { font-variant-numeric: tabular-nums; color: #c9cdd6; }
</style>
</head>
<body>
  <header>
    <h1>CommStats Report</h1>
    <div class="muted">Generated {{.Doc.GeneratedAt}} · {{.Doc.Span}}</div>
  </header>

  <div class="tabs" id="tabs"></div>
  <div id="panels"></div>

<script>
const DATA = {{.JSON}};
const PALETTE = ["#5b9bd5","#e0a458","#70ad47","#c0504d","#9b59b6","#4bc0c0","#f06292","#a0a0a0","#8dd3c7","#fb8072"];
const color = i => PALETTE[i % PALETTE.length];
const charts = [];

function el(tag, cls, html) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (html !== undefined) e.innerHTML = html;
  return e;
}

function makeCanvas(card) {
  const c = document.createElement("canvas");
  card.appendChild(c);
  return c;
}

// --- chart builders by kind ---

function renderSeriesChart(card, chart) {
  const periods = chart.periods || [];
  const ctrl = el("div", "controls");
  const sel = document.createElement("select");
  periods.forEach((p, i) => {
    const o = document.createElement("option");
    o.value = i; o.textContent = p.period; sel.appendChild(o);
  });
  ctrl.appendChild(document.createTextNode("Granularity: "));
  ctrl.appendChild(sel);
  card.appendChild(ctrl);
  const cv = makeCanvas(card);
  const mk = i => ({
    type: "line",
    data: { labels: (periods[i]||{}).labels || [],
      datasets: [{ label: chart.title, data: (periods[i]||{}).data || [],
                   borderColor: color(0), backgroundColor: color(0), tension: 0.25 }] },
    options: { responsive: true, plugins: { legend: { display: false } } }
  });
  const ch = new Chart(cv, mk(0));
  charts.push(ch);
  sel.addEventListener("change", () => { const c = mk(sel.value); ch.config.data = c.data; ch.update(); });
}

function renderBars(card, chart, horizontal) {
  const cv = makeCanvas(card);
  const bars = chart.bars || chart.top || [];
  charts.push(new Chart(cv, {
    type: "bar",
    data: { labels: bars.map(b => b.label),
      datasets: [{ label: chart.title, data: bars.map(b => b.value),
                   backgroundColor: bars.map((_, i) => color(i)) }] },
    options: { indexAxis: horizontal ? "y" : "x", responsive: true,
               plugins: { legend: { display: false } } }
  }));
}

function renderChart(panel, chart) {
  const card = el("div", "card");
  if (chart.kind === "topn" || chart.kind === "series") card.classList.add("wide");
  card.appendChild(el("h3", null, chart.title));
  switch (chart.kind) {
    case "series":    renderSeriesChart(card, chart); break;
    case "ordered":   renderBars(card, chart, false); break;
    case "breakdown": renderBars(card, chart, true); break;
    case "topn":      renderBars(card, chart, true); break;
  }
  panel.appendChild(card);
}

// --- overview ---

function renderOverview(panel) {
  const ov = DATA.overview || {};
  // Headline cards.
  const hl = el("div", "headlines");
  (ov.sources || []).forEach(s => {
    const card = el("div", "headline");
    card.appendChild(el("h4", null, s.label || s.source));
    (s.totals || []).forEach(t => {
      const row = el("div", "row");
      row.appendChild(el("span", null, t.label));
      row.appendChild(el("span", "n", Math.round(t.value).toLocaleString()));
      card.appendChild(row);
    });
    hl.appendChild(card);
  });
  panel.appendChild(hl);

  // Combined activity charts (weekday + hour), stacked per source.
  const grid = el("div", "grid");
  const stacked = (title, series) => {
    const card = el("div", "card wide");
    card.appendChild(el("h3", null, title));
    const cv = makeCanvas(card);
    charts.push(new Chart(cv, {
      type: "bar",
      data: { labels: (series && series.labels) || [],
        datasets: ((series && series.datasets) || []).map((d, i) => ({ label: d.name, data: d.data, backgroundColor: color(i) })) },
      options: { responsive: true, scales: { x: { stacked: true }, y: { stacked: true } } }
    }));
    grid.appendChild(card);
  };
  stacked("Activity by day of week (per source)", ov.weekday);
  stacked("Activity by hour of day (per source)", ov.hour);
  panel.appendChild(grid);
}

// --- tab wiring ---

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
    tab.classList.add("active");
    panel.classList.add("active");
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

// renderTables appends a collapsed <details> with each chart's underlying
// numbers as a table — a raw-data view and a fallback when charts don't load.
function renderTables(panel, s) {
  const det = el("details");
  const open = !window.Chart; // expanded when charts are unavailable
  if (open) det.setAttribute("open", "");
  det.appendChild(el("summary", null, "Raw data tables"));
  (s.charts || []).forEach(c => {
    det.appendChild(el("h3", null, c.title));
    if (c.kind === "series") {
      const p = (c.periods || [])[0] || { labels: [], data: [] };
      det.appendChild(kvTable("bucket", "value", (p.labels||[]).map((l,i)=>[l, (p.data||[])[i]])));
    } else {
      const rows = (c.bars || c.top || []).map(b => [b.label, b.value]);
      det.appendChild(kvTable(c.kind === "topn" ? "name" : "bucket", "count", rows));
    }
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
