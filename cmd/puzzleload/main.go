// Command puzzleload bulk loads the Lichess CC0 puzzle database into Postgres.
//
//	zstd -dc lichess_db_puzzle.csv.zst | puzzleload
//
// The file is 6M rows and 300MB compressed, so it is read from a stream rather
// than downloaded into the program. Re-running it against a newer dump is the
// intended way to refresh ratings: the load merges on puzzle id and leaves the
// counters this site keeps about its own users alone.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/puzzle"
	"github.com/leozh0u/blundernet/internal/store"
)

func main() {
	var (
		file          = flag.String("file", "", "CSV file to read, default stdin")
		limit         = flag.Int("limit", 0, "stop after this many puzzles, 0 for all")
		minPopularity = flag.Int("min-popularity", -100, "drop puzzles below this Lichess popularity score")
		maxDeviation  = flag.Float64("max-rating-deviation", 0, "drop puzzles whose rating is less certain than this, 0 for all")
	)
	flag.Parse()

	obs.SetupLogging("puzzleload")

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL is not set")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, dbURL, *file, *limit, *minPopularity, *maxDeviation); err != nil {
		slog.Error("load failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dbURL, file string, limit, minPopularity int, maxDeviation float64) error {
	in := os.Stdin
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}

	opts := []puzzle.ReaderOption{
		puzzle.WithLimit(limit),
		puzzle.WithFilter(func(p puzzle.Puzzle) bool {
			if p.Popularity < minPopularity {
				return false
			}
			// A high deviation means Lichess is not sure of the rating yet,
			// which matters because the rating is what both modes select on.
			return maxDeviation == 0 || p.RatingDev <= maxDeviation
		}),
	}
	src, err := puzzle.NewReader(in, opts...)
	if err != nil {
		return err
	}

	archive, err := store.NewArchive(ctx, dbURL)
	if err != nil {
		return err
	}
	defer archive.Close()
	puzzles := store.NewPuzzles(archive.Pool())

	start := time.Now()
	copied, merged, err := puzzles.Load(ctx, src)
	if err != nil {
		return err
	}
	loaded := time.Since(start)

	if err := puzzles.Analyze(ctx); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	total, err := puzzles.Count(ctx)
	if err != nil {
		return err
	}

	slog.Info("loaded puzzles",
		"copied", copied,
		"merged", merged,
		"skipped", src.Skipped(),
		"total_in_table", total,
		"seconds", loaded.Round(time.Second).Seconds(),
		"rows_per_second", int(float64(copied)/loaded.Seconds()))
	return nil
}
