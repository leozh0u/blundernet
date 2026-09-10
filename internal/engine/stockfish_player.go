package engine

import (
	"context"
	"errors"
	"time"
)

// Stockfish as an opponent rather than as a judge.
//
// The trained network is the point of this project and stays the default, but
// it plays around 1000 Elo, which is a poor game for anyone who can already
// play. Stockfish is already in the stack for post-game review, so offering it
// as the hard side costs one adapter rather than a second dependency.
//
// Deliberately a separate process from the review Stockfish. They contend
// otherwise: a review is up to twenty seconds of search and a move has three,
// so sharing one engine would make every hard game wait behind somebody's
// pasted game.
type StockfishPlayer struct {
	sf   *Stockfish
	wait time.Duration
}

// NewStockfishPlayer starts an engine for playing. MoveTime is the strength
// knob: search is what makes it good, so time is the honest dial.
func NewStockfishPlayer(opts StockfishOptions) (*StockfishPlayer, error) {
	sf, err := NewStockfish(opts)
	if err != nil {
		return nil, err
	}
	return &StockfishPlayer{sf: sf, wait: 4 * time.Second}, nil
}

func (p *StockfishPlayer) BestMove(fen string) (string, error) {
	// BestMove carries no context, and a wedged engine must not hold a worker
	// goroutine open forever. The bound is well above the move time so it
	// only fires when something is actually wrong.
	ctx, cancel := context.WithTimeout(context.Background(), p.wait)
	defer cancel()

	a, err := p.sf.Analyse(ctx, fen)
	if err != nil {
		return "", err
	}
	if a.Mated() {
		return "", errors.New("no legal move")
	}
	return a.Best, nil
}

func (p *StockfishPlayer) Name() string { return "stockfish" }

func (p *StockfishPlayer) Close() error { return p.sf.Close() }
