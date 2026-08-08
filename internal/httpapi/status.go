package httpapi

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/store"
)

// The targets published in the README. Kept here as data so the page shows
// the same numbers the README promises and a change has to happen in one
// place.
const (
	targetAPIP99ms  = 50
	targetEngineP95 = 3 * time.Second
	targetErrorRate = 0.001
)

// Status is what /api/status returns. Totals come from Postgres and are
// true for the whole service; latency and job counts come from this
// process's registry and describe one instance.
type Status struct {
	Version   string          `json:"version"`
	Uptime    string          `json:"uptime"`
	Checks    []Check         `json:"checks"`
	Games     *store.Stats    `json:"games,omitempty"`
	Workers   int             `json:"workers"`
	Engine    string          `json:"engine,omitempty"`
	Instance  InstanceMetrics `json:"instance"`
	Degraded  bool            `json:"degraded"`
	CheckedAt time.Time       `json:"checked_at"`
}

type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

type InstanceMetrics struct {
	Requests       uint64  `json:"requests"`
	ServerErrors   uint64  `json:"server_errors"`
	ErrorRate      float64 `json:"error_rate"`
	APIP50ms       float64 `json:"api_p50_ms"`
	APIP99ms       float64 `json:"api_p99_ms"`
	APISamples     uint64  `json:"api_samples"`
	EngineP50s     float64 `json:"engine_p50_s"`
	EngineP95s     float64 `json:"engine_p95_s"`
	EngineSamples  uint64  `json:"engine_samples"`
	WSConnections  float64 `json:"ws_connections"`
	JobsPlayed     uint64  `json:"jobs_played"`
	JobsDeduped    uint64  `json:"jobs_deduped"`
	MeetsAPITarget bool    `json:"meets_api_target"`
	MeetsEngineSLO bool    `json:"meets_engine_target"`
	MeetsErrorSLO  bool    `json:"meets_error_target"`
}

func (s *Server) status(ctx context.Context) Status {
	// Each dependency gets its own short deadline. A status page that hangs
	// because the thing it is reporting on hung is not a status page.
	checks := []Check{s.checkRedis(ctx), s.checkArchive(ctx)}

	st := Status{
		Version:   Version,
		Checks:    checks,
		CheckedAt: time.Now().UTC(),
	}
	for _, c := range checks {
		if !c.OK {
			st.Degraded = true
		}
	}

	if s.archive != nil {
		dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if stats, err := s.archive.Stats(dbCtx); err == nil {
			st.Games = stats
		}
	}

	snap := obs.Gather()
	st.Uptime = shortDuration(snap.Uptime)
	st.Instance = InstanceMetrics{
		Requests:      snap.Requests,
		ServerErrors:  snap.ServerErrors,
		APIP50ms:      snap.APILatency.P50 * 1000,
		APIP99ms:      snap.APILatency.P99 * 1000,
		APISamples:    snap.APILatency.Count,
		WSConnections: snap.WSOpen,
	}

	// The engine runs in the worker, so its timings come from whichever
	// workers are currently reporting rather than from this registry.
	reportCtx, cancelReports := context.WithTimeout(ctx, time.Second)
	defer cancelReports()
	if reports, err := s.games.LiveEngineReports(reportCtx); err == nil {
		q, played, deduped := obs.MergeEngineReports(reports)
		st.Workers = len(reports)
		st.Engine = engineNameOf(reports)
		st.Instance.EngineP50s = q.P50
		st.Instance.EngineP95s = q.P95
		st.Instance.EngineSamples = q.Count
		st.Instance.JobsPlayed = played
		// Expired, stale and conflict are all the idempotency logic
		// declining to act twice, so they read better as one number.
		st.Instance.JobsDeduped = deduped
	}
	// No requests yet is not a violation: zero out of zero is not an error
	// rate, so the target holds until there is something to divide by.
	st.Instance.MeetsErrorSLO = true
	if snap.Requests > 0 {
		st.Instance.ErrorRate = float64(snap.ServerErrors) / float64(snap.Requests)
		st.Instance.MeetsErrorSLO = st.Instance.ErrorRate <= targetErrorRate
	}
	// An unmeasured target is not a met target, so both of these require
	// samples before they can pass.
	st.Instance.MeetsAPITarget = st.Instance.APISamples > 0 &&
		st.Instance.APIP99ms < targetAPIP99ms
	st.Instance.MeetsEngineSLO = st.Instance.EngineSamples > 0 &&
		st.Instance.EngineP95s < targetEngineP95.Seconds()

	return st
}

func (s *Server) checkRedis(ctx context.Context) Check {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		return Check{Name: "redis", OK: false, Note: "live game state unreachable"}
	}
	return Check{Name: "redis", OK: true}
}

func (s *Server) checkArchive(ctx context.Context) Check {
	if s.archive == nil {
		return Check{Name: "postgres", OK: true, Note: "not configured"}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := s.archive.Stats(ctx); err != nil {
		return Check{Name: "postgres", OK: false, Note: "archive unreachable"}
	}
	return Check{Name: "postgres", OK: true}
}

func (s *Server) handleStatusJSON(w http.ResponseWriter, r *http.Request) {
	st := s.status(r.Context())
	code := http.StatusOK
	if st.Degraded {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, st)
}

func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	st := s.status(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := statusTmpl.Execute(w, st); err != nil {
		slog.Error("render status", "err", err)
	}
}

// engineNameOf reports the engine the workers are running, or says so when
// they disagree, which happens mid-deploy while both versions are up.
func engineNameOf(reports []obs.EngineReport) string {
	if len(reports) == 0 {
		return ""
	}
	name := reports[0].Engine
	for _, r := range reports[1:] {
		if r.Engine != name {
			return "mixed"
		}
	}
	return name
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// Server-rendered with no scripts and no stylesheet request, so the page
// still answers when the frontend bundle is missing or broken. A status
// page that depends on the app it reports on cannot tell you the app is
// down.
var statusTmpl = template.Must(template.New("status").Funcs(template.FuncMap{
	"ms":      func(f float64) string { return fmt.Sprintf("%.0f ms", f) },
	"secs":    func(f float64) string { return fmt.Sprintf("%.2f s", f) },
	"pct":     func(f float64) string { return fmt.Sprintf("%.3f%%", f*100) },
	"verdict": func(ok bool) string { return map[bool]string{true: "within target", false: "over target"}[ok] },
}).Parse(statusHTML))

const statusHTML = `<!doctype html>
<title>BlunderNet status</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:44rem;margin:3rem auto;padding:0 1rem;color:#111}
 @media(prefers-color-scheme:dark){body{background:#111;color:#eee}td,th{border-color:#333}}
 h1{font-size:1.4rem;margin:0 0 .25rem}
 .sub{color:#777;margin:0 0 2rem}
 table{border-collapse:collapse;width:100%;margin:0 0 2rem}
 th,td{text-align:left;padding:.5rem .25rem;border-bottom:1px solid #e5e5e5}
 th{font-weight:600;width:55%}
 .ok{color:#0a7d32}.bad{color:#b3261e}
 .note{color:#777;font-size:.85rem}
 footer{color:#777;font-size:.85rem}
</style>
<h1>BlunderNet status</h1>
<p class="sub">
{{if .Degraded}}<strong class="bad">Degraded</strong>{{else}}<strong class="ok">Operational</strong>{{end}}
&middot; up {{.Uptime}} &middot; v{{.Version}}
</p>

<table>
<tr><th colspan="2">Dependencies</th></tr>
{{range .Checks}}
<tr><th>{{.Name}}</th><td>
 {{if .OK}}<span class="ok">ok</span>{{else}}<span class="bad">failing</span>{{end}}
 {{with .Note}}<span class="note">{{.}}</span>{{end}}
</td></tr>
{{end}}
</table>

{{with .Games}}
<table>
<tr><th colspan="2">Games played, all time</th></tr>
<tr><th>Finished</th><td>{{.Total}}</td></tr>
<tr><th>Engine wins</th><td>{{.EngineWins}}</td></tr>
<tr><th>Player wins</th><td>{{.PlayerWins}}</td></tr>
<tr><th>Draws</th><td>{{.Draws}}</td></tr>
</table>
{{end}}

<table>
<tr><th colspan="2">Since restart</th></tr>
<tr><th>Requests</th><td>{{.Instance.Requests}}</td></tr>
<tr><th>Server errors <span class="note">target under 0.1%</span></th>
    <td>{{.Instance.ServerErrors}} ({{pct .Instance.ErrorRate}})
        <span class="{{if .Instance.MeetsErrorSLO}}ok{{else}}bad{{end}}">{{verdict .Instance.MeetsErrorSLO}}</span></td></tr>
<tr><th>API latency p50</th><td>{{ms .Instance.APIP50ms}}</td></tr>
<tr><th>API latency p99 <span class="note">target under 50 ms</span></th>
    <td>{{if .Instance.APISamples}}{{ms .Instance.APIP99ms}}
        <span class="{{if .Instance.MeetsAPITarget}}ok{{else}}bad{{end}}">{{verdict .Instance.MeetsAPITarget}}</span>
        {{else}}<span class="note">no samples yet</span>{{end}}</td></tr>
<tr><th>Engine reply p50</th>
    <td>{{if .Instance.EngineSamples}}{{secs .Instance.EngineP50s}}{{else}}<span class="note">no samples yet</span>{{end}}</td></tr>
<tr><th>Engine reply p95 <span class="note">target under 3 s</span></th>
    <td>{{if .Instance.EngineSamples}}{{secs .Instance.EngineP95s}}
        <span class="{{if .Instance.MeetsEngineSLO}}ok{{else}}bad{{end}}">{{verdict .Instance.MeetsEngineSLO}}</span>
        {{else}}<span class="note">no samples yet</span>{{end}}</td></tr>
<tr><th>Open WebSockets <span class="note">this instance</span></th><td>{{printf "%.0f" .Instance.WSConnections}}</td></tr>
<tr><th>Workers reporting</th><td>{{.Workers}}{{with .Engine}} <span class="note">{{.}}</span>{{end}}</td></tr>
<tr><th>Engine moves played</th><td>{{.Instance.JobsPlayed}}</td></tr>
<tr><th>Duplicate jobs declined <span class="note">at-least-once delivery, dropped safely</span></th>
    <td>{{.Instance.JobsDeduped}}</td></tr>
</table>

<footer>
Counters reset when the instance restarts. Game totals come from Postgres and
survive restarts. Percentiles are read off histogram buckets, so they are only
as precise as the bucket edges. Checked {{.CheckedAt.Format "2006-01-02 15:04:05 UTC"}}.
Machine-readable at <a href="/api/status">/api/status</a>.
</footer>
`
