package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/httpapi"
	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/store"
	"github.com/leozh0u/blundernet/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	obs.SetupLogging("api")

	opts, err := redis.ParseURL(envOr("REDIS_URL", "redis://localhost:6379"))
	if err != nil {
		fatal("redis url", err)
	}
	rdb := redis.NewClient(opts)
	games := store.NewGames(rdb)

	archive, err := store.NewArchive(ctx, envOr("DATABASE_URL",
		"postgres://blundernet:blundernet@localhost:5432/blundernet"))
	if err != nil {
		fatal("postgres", err)
	}
	defer archive.Close()

	jobs, err := queue.New(ctx)
	if err != nil {
		fatal("queue", err)
	}

	srv := &http.Server{
		Addr: ":" + envOr("PORT", "8080"),
		Handler: obs.Middleware(httpapi.New(httpapi.Deps{
			Games:         games,
			Archive:       archive,
			Users:         store.NewUsers(archive.Pool()),
			Jobs:          jobs,
			Redis:         rdb,
			Static:        web.Dist(),
			SecureCookies: envOr("SECURE_COOKIES", "true") != "false",
			// Off by default. Reading X-Forwarded-For is only safe when
			// something we control terminates the connection first, and
			// getting that wrong hands every client a free rate-limit reset.
			TrustProxy: envOr("TRUST_PROXY", "false") == "true",
			Limits: httpapi.Limits{
				Auth:       limitFromEnv("RATE_LIMIT_AUTH"),
				CreateGame: limitFromEnv("RATE_LIMIT_CREATE"),
				Move:       limitFromEnv("RATE_LIMIT_MOVE"),
			},
		})),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			fatal("serve", err)
		}
	}()

	// Metrics listen on their own port so that /metrics is never routed
	// through the load balancer and stays off the public surface.
	metricsAddr := ":" + envOr("METRICS_PORT", "9090")
	go func() {
		slog.Info("metrics listening", "addr", metricsAddr)
		if err := obs.ServeMetrics(metricsAddr); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server stopped", "err", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// limitFromEnv reads "burst,per-second", for example "10,0.5". An unset or
// unparseable value returns the zero limit, which the server reads as "use
// the default" rather than "allow nothing". Failing open matters here: a typo
// in an environment variable should not lock everyone out of the site.
func limitFromEnv(key string) store.Limit {
	raw := os.Getenv(key)
	if raw == "" {
		return store.Limit{}
	}
	burstStr, rateStr, ok := strings.Cut(raw, ",")
	if !ok {
		slog.Warn("ignoring malformed rate limit", "key", key, "value", raw)
		return store.Limit{}
	}
	burst, err1 := strconv.Atoi(strings.TrimSpace(burstStr))
	rate, err2 := strconv.ParseFloat(strings.TrimSpace(rateStr), 64)
	if err1 != nil || err2 != nil || burst <= 0 || rate <= 0 {
		slog.Warn("ignoring malformed rate limit", "key", key, "value", raw)
		return store.Limit{}
	}
	return store.Limit{Burst: burst, Rate: rate}
}

// fatal logs through the same JSON handler as everything else and exits.
// log.Fatalf would write an unstructured line, which is the one log entry
// that most needs to survive the pipeline.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
