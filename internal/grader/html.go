package grader

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

// reportCSS is the whole stylesheet of the scorecard, inlined.
//
// Inline and nothing else: no CDN, no webfont fetch, no script. The page must
// render on a laptop with no network, out of a tarball, six months from now,
// because that is when somebody re-reads a debrief. It is deliberately plain -
// this is an internal tool, not a brand surface - and it borrows exactly one
// habit from the brand book, separation by spacing rather than by rules, because
// a table full of borders is harder to read.
const reportCSS = `
:root { color-scheme: light; }
* { box-sizing: border-box; }
body { margin: 0; padding: 32px 28px 64px; background: #fff; color: #1b1b1b;
       font: 14px/1.55 -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
main { max-width: 1180px; margin: 0 auto; }
h1 { font-size: 26px; margin: 0 0 4px; font-weight: 700; }
h2 { font-size: 18px; margin: 40px 0 10px; font-weight: 700; }
h3 { font-size: 15px; margin: 24px 0 6px; font-weight: 700; }
p { margin: 6px 0; }
.sub { color: #555; margin-bottom: 24px; }
.headline { display: flex; gap: 28px; align-items: baseline; flex-wrap: wrap; margin: 12px 0 4px; }
.total { font-size: 40px; font-weight: 700; letter-spacing: -0.5px; }
.of { color: #666; font-size: 18px; }
.badge { display: inline-block; padding: 2px 9px; border-radius: 3px; font-size: 12px;
         font-weight: 700; text-transform: uppercase; letter-spacing: 0.4px; }
.badge.pass { background: #e2f4e6; color: #1c5c2c; }
.badge.fail { background: #fbe4e4; color: #7d1d1d; }
.badge.skip { background: #eeeeee; color: #4a4a4a; }
.badge.error { background: #f6e7d2; color: #6d4210; }
table { border-collapse: collapse; width: 100%; margin: 10px 0 6px; font-size: 13px; }
th, td { text-align: left; padding: 6px 10px 6px 0; vertical-align: top; }
th { font-weight: 700; border-bottom: 1px solid #d8d8d8; }
td { border-bottom: 1px solid #f0f0f0; }
td.num, th.num { text-align: right; padding-right: 18px; white-space: nowrap; }
.scroll { overflow-x: auto; }
code, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.finding { margin: 8px 0 8px 0; padding-left: 12px; border-left: 3px solid #e3e3e3; }
.finding .k { color: #666; display: inline-block; min-width: 68px; }
.hint { color: #555; font-style: italic; }
.callout { padding: 12px 14px; border-radius: 4px; margin: 10px 0; }
.callout.dq { background: #fbe4e4; }
.callout.warn { background: #fdf3e0; }
.callout.note { background: #f2f2f2; }
.callout.harness { background: #eef2fb; }
ul { margin: 6px 0 6px 18px; padding: 0; }
li { margin: 3px 0; }
a { color: #1a4fbf; }
.evidence { color: #666; }
.muted { color: #777; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 14px; margin: 12px 0; }
.card { padding: 12px 14px; background: #f7f8fa; border-radius: 4px; }
.card .label { font-size: 12px; color: #555; text-transform: uppercase; letter-spacing: 0.4px; }
.card .value { font-size: 20px; font-weight: 700; margin-top: 2px; }
.bar { height: 6px; background: #e6e6e6; border-radius: 3px; margin-top: 8px; overflow: hidden; }
.bar > span { display: block; height: 100%; background: #2f6fd0; }
`

// reportTemplate is the scorecard.
//
// It is one page, in reading order: verdict, then the things that change a
// decision (harness events, disqualifiers, failures), then the score by block,
// then the counter cross-check, then the full assertion table, then the links a
// reviewer clicks to look at the systems themselves.
var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"pct": func(a, b int) int {
		if b <= 0 {
			return 0
		}
		return a * 100 / b
	},
	// upper takes any and not string on purpose: Status and Level are named
	// string types, and text/template will not convert a named type to a string
	// parameter. Typing this as string cost a rendered report once already, and
	// the failure surfaced only at the end of a fifteen-minute scoring run.
	"upper": func(v any) string { return strings.ToUpper(fmt.Sprint(v)) },
}).Parse(`<title>{{.Title}}</title>
<style>` + reportCSS + `</style>
<main>
<h1>{{.Title}}</h1>
<p class="sub">Reviewer scorecard. Renders offline; no external requests. Budget 45 minutes.</p>

<div class="headline">
  <div><span class="total">{{.Score.Total}}</span> <span class="of">/ {{.Score.Budget}}</span></div>
  <div>
    <span class="badge {{if .Score.Passed}}pass{{else}}fail{{end}}">onsite: {{if .Score.Passed}}yes{{else}}not yet{{end}}</span>
    &nbsp;<strong>{{.Score.Band}}</strong>
  </div>
</div>
<p class="muted">{{.Score.BandReason}}</p>
<p class="muted">{{.Score.PassReason}}</p>
{{with .Score.Submission}}<p class="mono">submission: {{.}}</p>{{end}}
{{with .Score.Connector}}<p class="mono">connector: {{.}}</p>{{end}}

{{if .Score.HarnessErrors}}
<h2>Harness events</h2>
<div class="callout harness">
<p>These are failures of the grading harness, not of the submission. They carry no
points, and a run that produced them was not fully assessed.</p>
<ul>{{range .Score.HarnessErrors}}<li>{{.}}</li>{{end}}</ul>
</div>
{{end}}

{{with .Disqualifiers}}
<h2>Automatic disqualifiers</h2>
{{range .}}<div class="callout dq">
<p><strong>{{.ID}}</strong> — {{.Message}}</p>
{{with .Evidence}}<p class="evidence mono">evidence: {{.}}</p>{{end}}
</div>{{end}}
{{end}}

{{with .Failures}}
<h2>Failures</h2>
{{range .}}
<h3>{{.Scenario}} / {{.Assertion.ID}} <span class="badge fail">{{.Earned}}/{{.Possible}}</span></h3>
{{with .Assertion.Title}}<p>{{.}}</p>{{end}}
<p>{{.Summary}}</p>
{{with .Assertion.Why}}<p class="hint">Why this is graded: {{.}}</p>{{end}}
{{range .Findings}}<div class="finding">
{{with .Subject}}<p class="mono"><strong>{{.}}</strong></p>{{end}}
<p><span class="k">expected</span> {{.Expected}}</p>
<p><span class="k">actual</span> {{.Actual}}</p>
{{with .Hint}}<p class="hint">{{.}}</p>{{end}}
{{with .EvidencePath}}<p class="evidence mono">evidence: <a href="file://{{.}}">{{.}}</a></p>{{end}}
</div>{{end}}
{{if .Truncated}}<p class="muted">... and {{.Truncated}} more findings; the full list is in the evidence file.</p>{{end}}
{{end}}
{{end}}

{{with .Warnings}}
<h2>Warnings</h2>
<div class="callout warn"><p>No points. Interview material.</p>
<ul>{{range .}}<li>{{.Message}}</li>{{end}}</ul></div>
{{end}}

<h2>Score by block</h2>
<div class="scroll">
<table>
<tr><th>Block</th><th class="num">Earned</th><th class="num">Assessed</th><th class="num">Budget</th><th></th><th>Note</th></tr>
{{range .Score.Blocks}}
<tr>
  <td>{{.Label}}</td>
  <td class="num">{{.Earned}}</td>
  <td class="num">{{.Possible}}</td>
  <td class="num">{{.Budget}}</td>
  <td style="min-width:120px"><div class="bar"><span style="width:{{pct .Earned .Budget}}%"></span></div></td>
  <td class="muted">{{.Note}}</td>
</tr>
{{end}}
<tr><td><strong>Total</strong></td><td class="num"><strong>{{.Score.Total}}</strong></td>
<td class="num"><strong>{{.Score.Possible}}</strong></td><td class="num"><strong>{{.Score.Budget}}</strong></td><td></td><td></td></tr>
</table>
</div>
{{if not .Score.DecisionLog}}
<div class="callout note"><p>The decision log has not been scored. It is the one
human-entered number in the model and the tool never guesses it: re-run with
<code>--decision-log-score N --decision-log-by NAME</code>.</p></div>
{{end}}

{{with .CounterRows}}
<h2>Counter cross-check</h2>
<p class="muted">The connector's self-reported <code>run.json</code> against the servers' own
metrics. Reported, never scored: BUILD-SPEC 17.6.6 settles that a disagreement is
a finding for the debrief and worth no points. The servers are the truth.</p>
<div class="scroll">
<table>
<tr><th>Scenario</th><th>Counter</th><th class="num">run.json</th><th class="num">server</th><th class="num">delta</th></tr>
{{range .}}<tr><td>{{.Scenario}}</td><td class="mono">{{.Name}}</td>
<td class="num">{{.Reported}}</td><td class="num">{{.Server}}</td>
<td class="num">{{if .Mismatch}}<span class="badge fail">{{.Delta}}</span>{{else}}{{.Delta}}{{end}}</td></tr>{{end}}
</table>
</div>
{{end}}

{{with .Score.GoTask}}
<h2>Go task: kredexp-2.1</h2>
{{if not .Implemented}}<div class="callout note"><p>The importer is the shipped stub. The Go
task scored zero; the pipeline was graded with the reference importer wherever a
scenario enabled it, so the two halves stayed independent.</p></div>{{end}}
<div class="scroll">
<table>
<tr><th>Fixture</th><th>Visibility</th><th>Result</th><th>Detect</th><th>Accepted</th><th>Diagnostics</th></tr>
{{range .Fixtures}}<tr>
<td class="mono">{{.Name}}</td>
<td>{{if .Hidden}}hidden{{else}}public{{end}}</td>
<td><span class="badge {{if .Passed}}pass{{else}}fail{{end}}">{{if .Passed}}pass{{else}}fail{{end}}</span></td>
<td>{{if .Detected}}yes{{else}}no{{end}}</td>
<td>{{.Accepted}}</td>
<td class="mono muted">{{range $i, $c := .Codes}}{{if $i}}, {{end}}{{$c}}{{end}}</td>
</tr>{{end}}
</table>
</div>
{{end}}

<h2>Every assertion</h2>
<div class="scroll">
<table>
<tr><th>Scenario</th><th>Assertion</th><th>Kind</th><th>Level</th><th>Status</th><th class="num">Points</th><th>Summary</th><th>Evidence</th></tr>
{{range .Score.Results}}<tr>
<td>{{.Scenario}}</td>
<td class="mono">{{.Assertion.ID}}</td>
<td class="mono muted">{{.Assertion.Kind}}</td>
<td class="muted">{{.Assertion.Level}}</td>
<td><span class="badge {{.Status}}">{{upper .Status}}</span></td>
<td class="num">{{.Earned}}/{{.Possible}}</td>
<td>{{.Summary}}</td>
<td class="evidence mono">{{range .Findings}}{{if .EvidencePath}}<a href="file://{{.EvidencePath}}">evidence</a>{{break}}{{end}}{{end}}</td>
</tr>{{end}}
</table>
</div>

<h2>The systems themselves</h2>
<p class="muted">Both service UIs are read-only, cost no quota and need no
authentication. They are the fastest way to answer "did the data actually arrive"
without reading a log. The addresses below are the ones the harness allocated for
each scenario; they are live only while that scenario's services are up, so a
scorecard read later is a record of where to look, not a working link.</p>
<div class="scroll">
<table>
<tr><th>Scenario</th><th>Twin UI</th><th>ERP UI</th><th>Output tree</th></tr>
{{range .Score.Runs}}<tr>
<td>{{.Scenario}}</td>
<td class="mono">{{index $.TwinUI .Scenario}}</td>
<td class="mono">{{index $.ERPUI .Scenario}}</td>
<td class="mono"><a href="file://{{.Dirs.Root}}">{{.Dirs.Root}}</a></td>
</tr>{{end}}
</table>
</div>

{{with .Notes}}
<h2>Notes</h2>
<div class="callout note"><ul>{{range .}}<li>{{.Message}}</li>{{end}}</ul></div>
{{end}}

{{with .Score.Stretch}}
<h2>Stretch scenarios</h2>
<p class="muted">Zero points, recorded as a tie-break note only.</p>
<ul>{{range .}}<li>{{.}}</li>{{end}}</ul>
{{end}}
</main>
`))

// counterRow is one row of the counter cross-check table.
type counterRow struct {
	Scenario string
	Name     string
	Reported string
	Server   string
	Delta    string
	Mismatch bool
}

// reportData is the template's view of a score.
type reportData struct {
	Title         string
	Score         *Score
	Disqualifiers []Flag
	Warnings      []Flag
	Notes         []Flag
	Failures      []Result
	CounterRows   []counterRow
	TwinUI, ERPUI map[string]string
}

// WriteReportHTML writes the self-contained reviewer scorecard.
func WriteReportHTML(path string, sc *Score) error {
	data := reportData{
		Title:         "Integrations challenge scorecard",
		Score:         sc,
		Disqualifiers: sc.Disqualifiers(),
		Failures:      sc.Failures(),
		TwinUI:        map[string]string{},
		ERPUI:         map[string]string{},
	}
	for _, f := range sc.Flags {
		switch f.Severity {
		case SeverityWarning:
			data.Warnings = append(data.Warnings, f)
		case SeverityNote:
			data.Notes = append(data.Notes, f)
		}
	}
	for _, r := range sc.Results {
		if r.Assertion.Kind != KindCounterCrosscheck {
			continue
		}
		for _, name := range sortedKeys(r.Info) {
			reported, server, ok := splitCrosscheck(r.Info[name])
			if !ok {
				continue
			}
			data.CounterRows = append(data.CounterRows, counterRow{
				Scenario: r.Scenario, Name: name,
				Reported: reported, Server: server,
				Delta:    crosscheckDelta(reported, server),
				Mismatch: reported != server,
			})
		}
	}
	for _, run := range sc.Runs {
		// The addresses are not retained after teardown, so the row names where
		// the run's tree is instead of pretending a dead port is a link.
		data.TwinUI[run.Scenario] = "started on 127.0.0.1 with an ephemeral port; " +
			"see logs/miniblp.log for the address it announced"
		data.ERPUI[run.Scenario] = "started on 127.0.0.1 with an ephemeral port; " +
			"see logs/erp.log for the address it announced"
	}
	var buf bytes.Buffer
	if err := reportTemplate.Execute(&buf, data); err != nil {
		return fmt.Errorf("grader: rendering report.html: %w", err)
	}
	return writeFile(path, buf.Bytes())
}

// splitCrosscheck parses the "reported N, server M" info string the
// counter_crosscheck check produces.
func splitCrosscheck(s string) (reported, server string, ok bool) {
	const sep = ", server "
	i := strings.Index(s, sep)
	if !strings.HasPrefix(s, "reported ") || i < 0 {
		return "", "", false
	}
	return strings.TrimPrefix(s[:i], "reported "), s[i+len(sep):], true
}

// crosscheckDelta renders the difference between two rendered integers.
func crosscheckDelta(reported, server string) string {
	a, err1 := parseIntish(reported)
	b, err2 := parseIntish(server)
	if err1 != nil || err2 != nil {
		return "-"
	}
	d := a - b
	if d > 0 {
		return "+" + formatInt(d)
	}
	return formatInt(d)
}
