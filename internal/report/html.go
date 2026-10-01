package report

import (
	"fmt"
	"html/template"
	"strings"
)

// RenderHTML produces a single self-contained HTML file: inline CSS, no CDN,
// no external assets. It must open straight from disk over file:// — which is
// why nothing is fetched from the network.
func (r *Report) RenderHTML() (string, error) {
	tmpl, err := template.New("opswatch").Funcs(funcs).Parse(htmlTmpl)
	if err != nil {
		return "", fmt.Errorf("parse html template: %w", err)
	}
	data := struct {
		Report  *Report
		Rows    []htmlRow
		Fails   int
		Warns   int
		Skips   int
		Total   int
		Stamp   string
		StampTS string
	}{
		Report:  r,
		Rows:    r.htmlRows(),
		Fails:   r.FailedCount(),
		Warns:   len(r.Warnings()),
		Skips:   r.SkippedCount(),
		Total:   len(r.Results),
		Stamp:   r.GeneratedAt.Format("02.01.2006 15:04:05"),
		StampTS: r.GeneratedAt.Format(timeLayout),
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("execute html template: %w", err)
	}
	return b.String(), nil
}

const timeLayout = "2006-01-02 15:04:05 MST"

type htmlRow struct {
	Status  string
	Cls     string
	Name    string
	Detail  string
}

// FailedCount is the number of broken checks.
func (r *Report) FailedCount() int {
	n := 0
	for _, x := range r.Results {
		if !x.OK && !x.Skipped {
			n++
		}
	}
	return n
}

// SkippedCount is the number of checks that do not apply on this host.
func (r *Report) SkippedCount() int {
	n := 0
	for _, x := range r.Results {
		if x.Skipped {
			n++
		}
	}
	return n
}

func (r *Report) htmlRows() []htmlRow {
	out := make([]htmlRow, 0, len(r.Results))
	for _, x := range r.Results {
		row := htmlRow{Name: x.Name, Detail: x.Detail}
		switch {
		case x.Skipped:
			row.Status, row.Cls = "skip", "skip"
		case !x.OK:
			row.Status, row.Cls = "FAIL", "fail"
		case x.Warn:
			row.Status, row.Cls = "warn", "warn"
		default:
			row.Status, row.Cls = "ok", "ok"
		}
		out = append(out, row)
	}
	// severity order, stable within a class
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if order(out[j].Cls) < order(out[i].Cls) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func order(cls string) int {
	switch cls {
	case "fail":
		return 0
	case "warn":
		return 1
	case "skip":
		return 2
	default:
		return 3
	}
}

const htmlTmpl = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Ccircle cx='8' cy='8' r='6' fill='%231a7f4b'/%3E%3C/svg%3E">
<title>OpsWatch — {{.Report.Host}} — {{.Stamp}}</title>
<style>
  :root {
    --bg: #f6f7f9; --panel: #ffffff; --ink: #1b1f24; --dim: #5c6773;
    --line: #e3e7ec; --ok: #1a7f4b; --okbg: #e6f4ec;
    --warn: #8a6100; --warnbg: #fdf3dd;
    --fail: #b3261e; --failbg: #fdeceb;
    --skip: #6b7280; --skipbg: #f0f1f3;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #14171a; --panel: #1c2024; --ink: #e8eaed; --dim: #9aa4ae;
      --line: #2a2f35; --ok: #5fd39b; --okbg: #163024;
      --warn: #e8b64c; --warnbg: #33280f;
      --fail: #f2837b; --failbg: #3a1c1a;
      --skip: #7b848e; --skipbg: #23262a;
    }
  }
  * { box-sizing: border-box; }
  body { margin: 0; padding: 32px 20px; background: var(--bg); color: var(--ink);
         font: 15px/1.55 -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif; }
  .wrap { max-width: 1080px; margin: 0 auto; }
  header { display: flex; flex-wrap: wrap; align-items: baseline; gap: 14px; margin-bottom: 6px; }
  h1 { font-size: 22px; font-weight: 650; margin: 0; letter-spacing: -.01em; }
  .host { color: var(--dim); font-size: 14px; }
  .badge { font-size: 12px; font-weight: 700; letter-spacing: .06em; padding: 3px 10px;
           border-radius: 999px; text-transform: uppercase; }
  .badge.ok  { color: var(--ok);   background: var(--okbg); }
  .badge.warn{ color: var(--warn); background: var(--warnbg); }
  .badge.fail{ color: var(--fail); background: var(--failbg); }
  .meta { color: var(--dim); font-size: 13px; margin-bottom: 26px; }
  section { background: var(--panel); border: 1px solid var(--line); border-radius: 10px;
            padding: 20px 22px; margin-bottom: 20px; }
  h2 { font-size: 13px; font-weight: 700; text-transform: uppercase; letter-spacing: .07em;
       color: var(--dim); margin: 0 0 14px; }
  table { width: 100%; border-collapse: collapse; font-size: 14px; }
  th { text-align: left; font-weight: 600; color: var(--dim); font-size: 12px;
       text-transform: uppercase; letter-spacing: .05em; padding: 0 10px 8px 0;
       border-bottom: 1px solid var(--line); }
  td { padding: 9px 10px 9px 0; border-bottom: 1px solid var(--line); vertical-align: top; }
  tr:last-child td { border-bottom: none; }
  .st { font-size: 11px; font-weight: 700; letter-spacing: .05em; padding: 2px 8px;
        border-radius: 4px; white-space: nowrap; }
  .st.ok { color: var(--ok); background: var(--okbg); }
  .st.warn { color: var(--warn); background: var(--warnbg); }
  .st.fail { color: var(--fail); background: var(--failbg); }
  .st.skip { color: var(--skip); background: var(--skipbg); }
  code { font-family: ui-monospace, "Cascadia Code", Consolas, monospace; font-size: 13px; }
  .name { white-space: nowrap; }
  .detail { color: var(--dim); }
  .num { text-align: right; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .empty { color: var(--dim); font-style: italic; }
  .money { display: flex; flex-wrap: wrap; gap: 10px; }
  .money div { background: var(--bg); border: 1px solid var(--line); border-radius: 8px;
               padding: 12px 16px; min-width: 170px; }
  .money b { display: block; font-size: 19px; font-weight: 650; font-variant-numeric: tabular-nums; }
  .money span { color: var(--dim); font-size: 12px; }
  footer { color: var(--dim); font-size: 12px; margin-top: 22px; }
  .note { color: var(--dim); font-size: 13px; margin-top: 10px; }
</style>
</head>
<body>
<div class="wrap">

<header>
  <h1>OpsWatch</h1>
  <span class="host">{{.Report.Host}}</span>
  <span class="badge {{.Report.Worst | lower}}">{{.Report.Worst}}</span>
</header>
<div class="meta">
  Сформирован {{.Stamp}} · проверок {{.Total }} ·
  ошибок {{.Fails}} · предупреждений {{.Warns}} · неприменимо {{.Skips}}
</div>

{{if .Report.Charges}}
<section>
  <h2>Ближайшие списания</h2>
  <table>
    <tr><th>Через</th><th>Дата</th><th class="num">Сумма</th><th>Что</th></tr>
    {{$r := .Report}}
    {{range $r.Charges}}
    <tr>
      <td>{{if eq .InDays 0}}сегодня{{else if eq .InDays 1}}завтра{{else if lt .InDays 0}}{{.InDays | neg}} дн. назад{{else}}через {{.InDays}} дн.{{end}}</td>
      <td class="num">{{.Date.Format "2006-01-02"}}</td>
      <td class="num">{{printf "%.0f" .Payment.Amount}} {{.Payment.Currency}}</td>
      <td><b>{{.Payment.Provider}}</b> — {{.Payment.Name}}</td>
    </tr>
    {{end}}
  </table>
</section>
{{else}}
<section>
  <h2>Ближайшие списания</h2>
  <p class="empty">В заданном окне ничего не списывается.</p>
</section>
{{end}}

<section>
  <h2>Расходы</h2>
  <div class="money">
    {{$c := .Report.Cost}}
    {{range $cur := $c.SortedCurrencies}}
    <div>
      <b>{{printf "%.0f" (index $c.Monthly $cur)}} {{$cur}}</b>
      <span>в месяц · {{printf "%.0f" (index $c.Yearly $cur)}} {{$cur}} в год</span>
    </div>
    {{end}}
  </div>
  {{if gt (len $c.Monthly) 1}}<p class="note">Валюты разведены намеренно: складывать рубли с долларами бессмысленно.</p>{{end}}
</section>

<section>
  <h2>Проверки</h2>
  <table>
    <tr><th></th><th>Что</th><th>Подробности</th></tr>
    {{range .Rows}}
    <tr>
      <td><span class="st {{.Cls}}">{{.Status}}</span></td>
      <td class="name"><code>{{.Name}}</code></td>
      <td class="detail">{{.Detail}}</td>
    </tr>
    {{end}}
  </table>
</section>

{{if .Report.Footer}}<footer>{{.Report.Footer}}</footer>{{end}}

</div>
</body>
</html>
`

// FuncMap for the template. Kept unexported so the surface stays small.
var funcs = template.FuncMap{
	"lower": strings.ToLower,
	"neg":   func(i int) int { return -i },
}
