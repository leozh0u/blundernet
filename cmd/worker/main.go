package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/engine"
	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/store"
	"github.com/leozh0u/blundernet/internal/worker"
)

func main() {
	// The worker image carries no HTTP client, so the binary probes its
	// own metrics port when the container healthcheck invokes it.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	obs.SetupLogging("worker")

	// The worker has no public surface, so metrics are the only thing it
	// serves. Same port as the api uses for its metrics listener, so one
	// scrape config covers both fleets.
	metricsAddr := ":" + envOr("METRICS_PORT", "9090")
	go func() {
		slog.Info("metrics listening", "addr", metricsAddr)
		if err := obs.ServeMetrics(metricsAddr); err != nil {
			slog.Error("metrics server stopped", "err", err)
		}
	}()

	opts, err := redis.ParseURL(envOr("REDIS_URL", "redis://localhost:6379"))
	if err != nil {
		fatal("redis url", err)
	}
	rdb := redis.NewClient(opts)

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

	games := store.NewGames(rdb)
	eng := engine.NewFromEnv()

	// Engine timings are measured here but published by the api, so they go
	// through Redis. HOSTNAME is the task or container id on ECS and compose.
	go store.PublishEngineReports(ctx, games, envOr("HOSTNAME", "worker"), eng.Name(), 15*time.Second)

	w := &worker.Worker{
		Games:   games,
		Archive: archive,
		Jobs:    jobs,
		Engine:  eng,
	}
	w.Run(ctx)
	slog.Info("worker stopped")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}

func healthcheck() int {
	resp, err := http.Get("http://127.0.0.1:" + envOr("METRICS_PORT", "9090") + "/metrics")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
