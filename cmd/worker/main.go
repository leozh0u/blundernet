package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/engine"
	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/review"
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

	// Stockfish reviews finished games. It is optional: a worker with no
	// Stockfish still plays, still serves hints, and simply cannot review,
	// which is a better failure than refusing to start.
	//
	// The move time is per position and a review asks about every position in
	// the game, so 120ms is roughly eight seconds for a sixty move game. That
	// is a queued job on a two core box shared with the site, and the number
	// to turn up the day reviews get their own machine.
	var analyser review.Analyser
	sf, err := engine.NewStockfish(engine.StockfishOptions{
		Path:     envOr("STOCKFISH_PATH", "stockfish"),
		MoveTime: envDuration("STOCKFISH_MOVETIME", 120*time.Millisecond),
	})
	if err != nil {
		slog.Warn("no analyser, reviews are disabled", "err", err)
	} else {
		defer sf.Close()
		analyser = sf
	}

	// Engine timings are measured here but published by the api, so they go
	// through Redis. HOSTNAME is the task or container id on ECS and compose.
	go store.PublishEngineReports(ctx, games, envOr("HOSTNAME", "worker"), eng.Name(), 15*time.Second)

	w := &worker.Worker{
		Games:    games,
		Archive:  archive,
		Analyser: analyser,
		Imports:  store.NewImports(archive.Pool()),
		Jobs:     jobs,
		Engine:   eng,
	}
	w.Run(ctx)
	slog.Info("worker stopped")
}

// envDuration reads a millisecond count, because a bare number in an env var
// is easier to get right than a Go duration string.
func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		slog.Warn("ignoring bad duration", "key", key, "value", v)
		return def
	}
	return time.Duration(ms) * time.Millisecond
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
