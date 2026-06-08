package report

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
)

// RenderHTML writes a self-contained interactive HTML report. The full data set
// is embedded as JSON and rendered with Chart.js (loaded from a CDN). Static
// fallback tables are always emitted so the report remains useful if the CDN is
// blocked or charts fail to initialize.
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

var htmlTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"num": func(v float64) string { return fmtVal(v, false) },
	"add": func(a, b int) int { return a + b },
}).Parse(htmlSource))

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
  body { font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 24px;
         background: #0f1117; color: #e6e6e6; }
  h1 { margin: 0 0 4px; font-size: 22px; }
  .muted { color: #8a8f98; font-size: 13px; }
  .src { margin-top: 32px; }
  .src > h2 { border-bottom: 1px solid #2a2e37; padding-bottom: 6px; }
  .grid { display: grid; grid-template-columns: 2fr 1fr; gap: 20px; margin-top: 16px; }
  .card { background: #161922; border: 1px solid #2a2e37; border-radius: 10px; padding: 16px; }
  .card h3 { margin: 0 0 12px; font-size: 14px; font-weight: 600; color: #c9cdd6; }
  .full { grid-column: 1 / -1; }
  .controls { margin-bottom: 12px; }
  select { background: #0f1117; color: #e6e6e6; border: 1px solid #2a2e37; border-radius: 6px;
           padding: 5px 8px; font-size: 13px; }
  canvas { max-height: 340px; }
  table { border-collapse: collapse; width: 100%; font-size: 13px; }
  th, td { text-align: right; padding: 5px 10px; border-bottom: 1px solid #232733; }
  th:first-child, td:first-child { text-align: left; }
  thead th { color: #8a8f98; font-weight: 600; }
  details { margin-top: 14px; }
  summary { cursor: pointer; color: #8a8f98; font-size: 13px; }
  .noscript { color: #e0a458; }
</style>
</head>
<body>
  <h1>CommStats Report</h1>
  <div class="muted">Generated {{.Doc.GeneratedAt}}</div>
  <noscript><p class="noscript">JavaScript is disabled — showing data tables only.</p></noscript>

  {{range $si, $s := .Doc.Sources}}
  <section class="src" data-source-index="{{$si}}">
    <h2>{{$s.Source}}</h2>

    <div class="grid">
      <div class="card full">
        <div class="controls">
          <label>Granularity:
            <select class="period-select" data-source="{{$si}}">
              {{range $pi, $p := $s.Periods}}<option value="{{$pi}}"{{if eq $pi 0}} selected{{end}}>{{$p.Period}}</option>{{end}}
            </select>
          </label>
        </div>
        <h3>Messages over time</h3>
        <canvas class="chart-timeline" data-source="{{$si}}"></canvas>
      </div>

      <div class="card">
        <h3>Messages by channel type (stacked)</h3>
        <canvas class="chart-stacked" data-source="{{$si}}"></canvas>
      </div>

      <div class="card">
        <h3>Channel type share</h3>
        <canvas class="chart-doughnut" data-source="{{$si}}"></canvas>
      </div>

      <div class="card">
        <h3>Avg messages sent per day, by weekday</h3>
        <canvas class="chart-weekday" data-source="{{$si}}"></canvas>
      </div>

      <div class="card full">
        <div class="controls">
          <label>Top lists range:
            <select class="top-select" data-source="{{$si}}">
              {{range $ri, $r := $s.TopRanges}}<option value="{{$ri}}"{{if eq $ri 0}} selected{{end}}>{{$r.Label}}</option>{{end}}
            </select>
          </label>
        </div>
      </div>

      <div class="card">
        <h3>Top channels</h3>
        <canvas class="chart-top-channels" data-source="{{$si}}"></canvas>
      </div>

      <div class="card">
        <h3>Top DMs</h3>
        <canvas class="chart-top-dms" data-source="{{$si}}"></canvas>
      </div>
    </div>

    <details>
      <summary>Data tables</summary>
      {{range $s.Periods}}
      <h3>{{.Period}}</h3>
      <table>
        <thead><tr><th>bucket</th>{{range .Labels}}<th>{{.}}</th>{{end}}</tr></thead>
        <tbody>
          <tr><td>messages_sent</td>{{range .MessagesSent}}<td>{{num .}}</td>{{end}}</tr>
          <tr><td>unique_channels</td>{{range .UniqueChannels}}<td>{{num .}}</td>{{end}}</tr>
          {{$series := .}}
          {{range $t := .Types}}<tr><td>{{$t}}</td>{{range index $series.ByType $t}}<td>{{num .}}</td>{{end}}</tr>{{end}}
        </tbody>
      </table>
      {{end}}
      <h3>By day of week</h3>
      <table>
        <thead><tr><th>weekday</th><th>total</th><th>avg/day</th></tr></thead>
        <tbody>
          {{range $s.Weekdays}}<tr><td>{{.Weekday}}</td><td>{{num .Total}}</td><td>{{num .Avg}}</td></tr>{{end}}
        </tbody>
      </table>
      {{range $s.TopRanges}}
      <h3>Top channels ({{.Label}})</h3>
      <table>
        <thead><tr><th>#</th><th>channel</th><th>type</th><th>messages</th></tr></thead>
        <tbody>
          {{range $i, $c := .Channels}}<tr><td>{{add $i 1}}</td><td>{{$c.Name}}</td><td>{{$c.Type}}</td><td>{{num $c.Total}}</td></tr>{{end}}
        </tbody>
      </table>
      <h3>Top DMs ({{.Label}})</h3>
      <table>
        <thead><tr><th>#</th><th>dm</th><th>type</th><th>messages</th></tr></thead>
        <tbody>
          {{range $i, $c := .DMs}}<tr><td>{{add $i 1}}</td><td>{{$c.Name}}</td><td>{{$c.Type}}</td><td>{{num $c.Total}}</td></tr>{{end}}
        </tbody>
      </table>
      {{end}}
    </details>
  </section>
  {{end}}

<script>
const DATA = {{.JSON}};

const PALETTE = ["#5b9bd5","#e0a458","#70ad47","#c0504d","#9b59b6","#4bc0c0","#f06292","#a0a0a0"];
function color(i){ return PALETTE[i % PALETTE.length]; }

const charts = {};

function timelineConfig(series){
  return {
    type: "line",
    data: {
      labels: series.labels || [],
      datasets: [
        { label: "messages sent", data: series.messages_sent || [], borderColor: color(0),
          backgroundColor: color(0), tension: 0.25, yAxisID: "y" },
        { label: "unique channels", data: series.unique_channels || [], borderColor: color(1),
          backgroundColor: color(1), tension: 0.25, yAxisID: "y1" }
      ]
    },
    options: {
      responsive: true,
      interaction: { mode: "index", intersect: false },
      scales: {
        y:  { position: "left",  title: { display: true, text: "messages" } },
        y1: { position: "right", title: { display: true, text: "channels" }, grid: { drawOnChartArea: false } }
      }
    }
  };
}

function stackedConfig(series){
  const types = series.types || [];
  return {
    type: "bar",
    data: {
      labels: series.labels || [],
      datasets: types.map((t,i) => ({ label: t, data: (series.by_type||{})[t] || [], backgroundColor: color(i) }))
    },
    options: { responsive: true, scales: { x: { stacked: true }, y: { stacked: true } } }
  };
}

function doughnutConfig(share){
  return {
    type: "doughnut",
    data: {
      labels: share.map(s => s.type),
      datasets: [{ data: share.map(s => s.total), backgroundColor: share.map((_,i)=>color(i)) }]
    },
    options: { responsive: true, plugins: { legend: { position: "bottom" } } }
  };
}

function topConfig(top){
  return {
    type: "bar",
    data: {
      labels: top.map(c => c.name),
      datasets: [{ label: "messages", data: top.map(c => c.total),
                   backgroundColor: top.map((c,i)=>color(i)) }]
    },
    options: { indexAxis: "y", responsive: true, plugins: { legend: { display: false } } }
  };
}

function weekdayConfig(days){
  return {
    type: "bar",
    data: {
      labels: days.map(d => d.weekday.slice(0,3)),
      datasets: [{ label: "avg messages/day", data: days.map(d => d.avg),
                   backgroundColor: days.map((_,i)=>color(i)) }]
    },
    options: { responsive: true, plugins: { legend: { display: false } } }
  };
}

function initSource(si){
  const s = DATA.sources[si];
  const q = sel => document.querySelector(sel + '[data-source="'+si+'"]');
  const tl = q(".chart-timeline"), st = q(".chart-stacked"),
        dn = q(".chart-doughnut"), tc = q(".chart-top-channels"),
        td = q(".chart-top-dms"), wd = q(".chart-weekday");

  const cur = () => parseInt(document.querySelector('.period-select[data-source="'+si+'"]').value, 10) || 0;
  const curTop = () => parseInt(document.querySelector('.top-select[data-source="'+si+'"]').value, 10) || 0;
  const ranges = s.top_ranges || [];
  const tr0 = ranges[curTop()] || { channels: [], dms: [] };

  charts[si] = {
    timeline:    new Chart(tl, timelineConfig(s.periods[cur()] || {})),
    stacked:     new Chart(st, stackedConfig(s.periods[cur()] || {})),
    doughnut:    new Chart(dn, doughnutConfig(s.type_share || [])),
    topChannels: new Chart(tc, topConfig(tr0.channels || [])),
    topDMs:      new Chart(td, topConfig(tr0.dms || [])),
    weekday:     new Chart(wd, weekdayConfig(s.weekdays || []))
  };

  document.querySelector('.period-select[data-source="'+si+'"]').addEventListener("change", () => {
    const series = s.periods[cur()] || {};
    const tlc = charts[si].timeline, sc = charts[si].stacked;
    tlc.data.labels = series.labels || [];
    tlc.data.datasets[0].data = series.messages_sent || [];
    tlc.data.datasets[1].data = series.unique_channels || [];
    tlc.update();
    sc.config.data = stackedConfig(series).data;
    sc.update();
  });

  document.querySelector('.top-select[data-source="'+si+'"]').addEventListener("change", () => {
    const tr = ranges[curTop()] || { channels: [], dms: [] };
    charts[si].topChannels.config.data = topConfig(tr.channels || []).data;
    charts[si].topChannels.update();
    charts[si].topDMs.config.data = topConfig(tr.dms || []).data;
    charts[si].topDMs.update();
  });
}

if (window.Chart) {
  DATA.sources.forEach((_, i) => initSource(i));
}
</script>
</body>
</html>
`
