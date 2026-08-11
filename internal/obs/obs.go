// Package obs holds the logging and metrics surface shared by the api and
// the worker. Both binaries emit JSON logs and expose the same Prometheus
// endpoint, so a single scrape config and a single log query work across
// the whole fleet.
package obs

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const ns = "blundernet"

var (
	// The route label is the ServeMux pattern, not the request path, so
	// /api/games/{id} is one series rather than one per game.
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Subsystem: "http", Name: "requests_total",
		Help: "HTTP requests by route, method and status class.",
	}, []string{"route", "method", "status"})

	// Buckets bottom out at 1ms because the target is a p99 under 30ms;
	// the client defaults start at 5ms and put that inside bucket two.
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Subsystem: "http", Name: "request_duration_seconds",
		Help:    "HTTP request duration by route and method.",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"route", "method"})

	wsConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "ws", Name: "connections",
		Help: "WebSocket connections currently open on this instance.",
	})

	gamesCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "games_created_total",
		Help: "Games created, by the colour the player chose.",
	}, []string{"color"})

	gamesFinished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "games_finished_total",
		Help: "Games that reached a terminal state, by result.",
	}, []string{"result"})

	// SQS is at-least-once, so "played" is not the only success: expired,
	// stale and conflict are the idempotency logic doing its job. Counting
	// them separately is what makes that behaviour visible.
	workerJobs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Subsystem: "worker", Name: "jobs_total",
		Help: "Job deliveries by outcome (played, expired, stale, conflict, error).",
	}, []string{"outcome"})

	// Rate limits are a guess until there is traffic to tune them against,
	// and the failure mode is silent: a limit set too tight just looks like
	// people not playing. Counting refusals by group, and separately for
	// signed-in and anonymous, is what makes that visible. Anonymous traffic
	// shares a bucket per address, so a university behind one NAT is the case
	// most likely to be squeezed.
	rateLimited = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "rate_limited_total",
		Help: "Requests refused by the rate limiter, by route group and whether the caller was signed in.",
	}, []string{"group", "identified"})

	// A search costs roughly a quarter second, so these buckets sit an
	// order of magnitude above the HTTP ones.
	engineDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: ns, Subsystem: "engine", Name: "move_duration_seconds",
		Help:    "Time for the engine to choose a move.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30},
	})
)

func init() {
	prometheus.MustRegister(
		httpRequests, httpDuration, wsConnections,
		gamesCreated, gamesFinished, workerJobs, engineDuration, rateLimited,
	)
}

// Constants rather than bare strings so a typo is a compile error instead
// of a silently separate time series.
const (
	JobPlayed   = "played"
	JobExpired  = "expired"
	JobStale    = "stale"
	JobConflict = "conflict"
	JobError    = "error"
)

func JobOutcome(outcome string)  { workerJobs.WithLabelValues(outcome).Inc() }
func EngineMove(d time.Duration) { engineDuration.Observe(d.Seconds()) }
func GameCreated(color string)   { gamesCreated.WithLabelValues(color).Inc() }
func GameFinished(result string) { gamesFinished.WithLabelValues(resultLabel(result)).Inc() }
func WSOpened()                  { wsConnections.Inc() }
func WSClosed()                  { wsConnections.Dec() }

// RateLimited records a refusal. identified separates signed-in callers, who
// get their own bucket, from anonymous ones sharing a bucket per address.
func RateLimited(group string, identified bool) {
	rateLimited.WithLabelValues(group, strconv.FormatBool(identified)).Inc()
}

// Collapsed to the three values a game can end in, so an unexpected
// result cannot open an unbounded label space.
func resultLabel(result string) string {
	switch result {
	case "1-0", "0-1", "1/2-1/2":
		return result
	default:
		return "other"
	}
}

// SetupLogging installs a JSON slog handler as the default logger. These
// logs land in CloudWatch or journald, where filtering on a field beats
// grepping a formatted string.
func SetupLogging(service string) *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	l := slog.New(h).With("service", service)
	slog.SetDefault(l)
	return l
}

// ServeMetrics starts the metrics listener and blocks until it fails. It
// gets its own listener rather than a route on the public mux, so scraping
// never goes through the load balancer and /metrics stays off the internet.
func ServeMetrics(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}

// statusRecorder captures the status code, which net/http does not expose
// after the handler has run.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack forwards to the underlying writer. gorilla/websocket type-asserts
// the ResponseWriter to http.Hijacker rather than going through
// http.ResponseController, so without this method every upgrade through
// this middleware fails with a 500.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	if r.status == 0 {
		r.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware records one counter increment and one duration observation per
// request, and logs anything that failed.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(status)).Inc()

		// A WebSocket connection lives as long as the game does, so timing
		// it would measure session length rather than service latency and
		// swamp the p99 the target is written against.
		if !isWebSocket(r) {
			httpDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		}

		if status >= 500 {
			slog.Error("request failed",
				"route", route, "method", r.Method, "status", status,
				"duration_ms", time.Since(start).Milliseconds())
		}
	})
}

func isWebSocket(r *http.Request) bool {
	return http.CanonicalHeaderKey(r.Header.Get("Upgrade")) == "Websocket"
}
