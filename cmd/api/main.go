package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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

// fatal logs through the same JSON handler as everything else and exits.
// log.Fatalf would write an unstructured line, which is the one log entry
// that most needs to survive the pipeline.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
