package review

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/leozh0u/blundernet/internal/engine"
)

// End to end against a real engine on a game whose ending is not a matter of
// opinion: Scholar's mate, where black's third move loses on the spot.
func TestRealEngineCatchesScholarsMate(t *testing.T) {
	if _, err := exec.LookPath("stockfish"); err != nil {
		t.Skip("stockfish is not installed")
	}
	sf, err := engine.NewStockfish(engine.StockfishOptions{MoveTime: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()

	// 1.e4 e5 2.Bc4 Nc6 3.Qh5 Nf6?? 4.Qxf7#
	moves := []string{"e2e4", "e7e5", "f1c4", "b8c6", "d1h5", "g8f6", "h5f7"}
	got, err := Game(context.Background(), sf, moves)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("white accuracy %.1f, black accuracy %.1f", got.WhiteAccuracy, got.BlackAccuracy)
	for _, m := range got.Moves {
		t.Logf("ply %d %-6s %-10s %.1f%% -> %.1f%%  better=%s",
			m.Ply, m.SAN, m.Judgement, m.WinBefore, m.WinAfter, m.BetterSAN)
	}

	// Nf6 is ply 6 and allows mate. It has to be the worst move in the game.
	if len(got.Worst) == 0 {
		t.Fatal("no worst move found in a game that ended in mate")
	}
	if got.Worst[0].Ply != 6 {
		t.Errorf("worst move is ply %d (%s), want ply 6 (Nf6)", got.Worst[0].Ply, got.Worst[0].SAN)
	}
	if got.Worst[0].Judgement != Blunder {
		t.Errorf("Nf6 was judged %q", got.Worst[0].Judgement)
	}
	if got.BlackAccuracy >= got.WhiteAccuracy {
		t.Errorf("black got mated but scored %.1f against white's %.1f", got.BlackAccuracy, got.WhiteAccuracy)
	}
}
