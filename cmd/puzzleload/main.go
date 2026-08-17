// Command puzzleload bulk loads the Lichess CC0 puzzle database into Postgres.
//
//	puzzleload -url https://database.lichess.org/lichess_db_puzzle.csv.zst
//	zstd -dc lichess_db_puzzle.csv.zst | puzzleload
//
// The file is 6M rows and 300MB compressed, so it is streamed and decompressed
// as it arrives rather than downloaded first. That matters on the deploy box,
// which has one gigabyte of memory and no reason to hold a 900MB CSV.
//
// Re-running it against a newer dump is the intended way to refresh ratings:
// the load merges on puzzle id and leaves the counters this site keeps about
// its own users alone.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/puzzle"
	"github.com/leozh0u/blundernet/internal/store"
)

func main() {
	var (
		file          = flag.String("file", "", "CSV file to read, default stdin")
		url           = flag.String("url", "", "fetch and decompress a .csv.zst from this URL instead")
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

	if err := run(ctx, dbURL, *file, *url, *limit, *minPopularity, *maxDeviation); err != nil {
		slog.Error("load failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dbURL, file, url string, limit, minPopularity int, maxDeviation float64) error {
	var in io.Reader = os.Stdin
	switch {
	case url != "":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("fetch %s: %s", url, res.Status)
		}
		zr, err := zstd.NewReader(res.Body)
		if err != nil {
			return err
		}
		defer zr.Close()
		in = zr
	case file != "":
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

	if err := puzzles.RefreshCells(ctx); err != nil {
		return fmt.Errorf("refresh cells: %w", err)
	}
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
