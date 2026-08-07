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

// Metric names are prefixed so they stay distinct from the Go runtime and
// process collectors the default registry already exposes.
const ns = "blundernet"

var (
	// RED metrics for the HTTP surface. The route label is the ServeMux
	// pattern rather than the request path: /api/games/{id} is one label
	// value, not one per game, which is what keeps cardinality bounded.
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Subsystem: "http", Name: "requests_total",
		Help: "HTTP requests by route, method and status class.",
	}, []string{"route", "method", "status"})

	// Buckets run from 1ms to 5s. The SLO for this API is a p99 under
	// 30ms, so the interesting resolution is at the bottom of the range;
	// the default client buckets start at 5ms and would put the target
	// inside the first two buckets.
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

	// Outcomes of a job delivery. SQS is at-least-once, so "played" is not
	// the only success: expired, stale and conflict are all correct
	// outcomes of the idempotency logic, and counting them separately is
	// what makes that logic observable instead of merely asserted.
	workerJobs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Subsystem: "worker", Name: "jobs_total",
		Help: "Job deliveries by outcome (played, expired, stale, conflict, error).",
	}, []string{"outcome"})

	// A search at the default simulation count costs roughly a quarter
	// second, so these buckets sit an order of magnitude above the HTTP
	// ones. Keeping them separate is the point of splitting inference off
	// the request path in the first place.
	engineDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: ns, Subsystem: "engine", Name: "move_duration_seconds",
		Help:    "Time for the engine to choose a move.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30},
	})
)

func init() {
	prometheus.MustRegister(
		httpRequests, httpDuration, wsConnections,
		gamesCreated, gamesFinished, workerJobs, engineDuration,
	)
}

// Job outcome labels, kept as constants so a typo is a compile error
// rather than a silently separate time series.
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

// resultLabel collapses the result string to the three values a chess game
// can end in, so an unexpected value cannot open an unbounded label space.
func resultLabel(result string) string {
	switch result {
	case "1-0", "0-1", "1/2-1/2":
		return result
	default:
		return "other"
	}
}

// SetupLogging installs a JSON slog handler as the default logger. JSON
// because these logs land in CloudWatch or journald, where filtering on a
// field beats grepping a formatted string.
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

// MetricsHandler serves the Prometheus exposition endpoint. It is mounted
// on its own listener rather than the public mux so that scraping does not
// go through the load balancer and /metrics is not reachable from the
// internet.
func MetricsHandler() http.Handler { return promhttp.Handler() }

// ServeMetrics starts the metrics listener and blocks until it fails.
func ServeMetrics(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", MetricsHandler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}

// statusRecorder captures the status code, which net/http does not expose
// after the fact. It also tracks whether the connection was hijacked, which
// a WebSocket upgrade does.
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

		// WebSocket requests are excluded from the latency histogram. The
		// connection lives as long as the game does, so timing it measures
		// session length, not service latency, and would swamp the p99 the
		// SLO is written against.
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
