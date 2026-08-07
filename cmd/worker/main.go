package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/engine"
	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/store"
	"github.com/leozh0u/blundernet/internal/worker"
)

func main() {
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
		log.Fatalf("redis url: %v", err)
	}
	rdb := redis.NewClient(opts)

	archive, err := store.NewArchive(ctx, envOr("DATABASE_URL",
		"postgres://blundernet:blundernet@localhost:5432/blundernet"))
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer archive.Close()

	jobs, err := queue.New(ctx)
	if err != nil {
		log.Fatalf("queue: %v", err)
	}

	w := &worker.Worker{
		Games:   store.NewGames(rdb),
		Archive: archive,
		Jobs:    jobs,
		Engine:  engine.NewFromEnv(),
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
