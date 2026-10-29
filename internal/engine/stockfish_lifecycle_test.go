package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeUCI(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "uci")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStockfishStartupHasADeadline(t *testing.T) {
	old := startupTimeout
	startupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { startupTimeout = old })
	path := fakeUCI(t, "while read line; do :; done\n")
	start := time.Now()
	if sf, err := NewStockfish(StockfishOptions{Path: path}); err == nil {
		_ = sf.Close()
		t.Fatal("silent engine started successfully")
	}
	if time.Since(start) > time.Second {
		t.Fatal("startup did not respect its deadline")
	}
}

func TestStockfishRestartsAfterCancelledAnalysis(t *testing.T) {
	t.Setenv("BLUNDERNET_UCI_MARKER", filepath.Join(t.TempDir(), "started"))
	path := fakeUCI(t, `
healthy=0
if [ -f "$BLUNDERNET_UCI_MARKER" ]; then healthy=1; else : > "$BLUNDERNET_UCI_MARKER"; fi
while read line; do
  case "$line" in
    uci) echo uciok ;;
    isready) echo readyok ;;
    go*)
      if [ "$healthy" = 1 ]; then echo 'info depth 1 score cp 20 pv e2e4'; echo 'bestmove e2e4'; else read ignored; fi ;;
    quit) exit 0 ;;
  esac
done
`)
	sf, err := NewStockfish(StockfishOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	const fen = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
	if _, err := sf.Analyse(ctx, fen); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stuck analysis returned %v", err)
	}
	a, err := sf.Analyse(context.Background(), fen)
	if err != nil || a.Best != "e2e4" {
		t.Fatalf("next request did not recover: %+v, %v", a, err)
	}
}
