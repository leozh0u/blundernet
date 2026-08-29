package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stockfish, spoken to over UCI on a pipe.
//
// It runs as its own process rather than being linked in, which is how UCI
// engines are meant to be used and also keeps this code clear of Stockfish's
// licence: nothing here is derived from it, we start it and talk to it.
//
// One process is kept alive for the life of the worker instead of started per
// analysis. Starting costs the process spawn plus loading the network, which
// is far more than analysing a single position, and a review asks about sixty
// questions in a row.
//
// Everything is serialised behind one mutex. UCI is a single conversation:
// there is no request id, so two callers interleaving "go" and reading lines
// would read each other's answers.

// Analysis is what the engine thinks of one position.
type Analysis struct {
	// Score in centipawns from the point of view of the side to move.
	// Positive means the side to move is better.
	CP int
	// Mate is the number of moves to mate, positive when the side to move is
	// giving it, negative when receiving. Zero when there is no forced mate.
	Mate int
	// Best is the move the engine would play, in UCI.
	Best string
}

// Mated reports whether the position is already over, which the engine
// answers by having no best move.
func (a Analysis) Mated() bool { return a.Best == "" || a.Best == "(none)" }

type Stockfish struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	closed bool

	// MoveTime is how long the engine gets per position. A review of a sixty
	// move game asks sixty times, so this is the number that decides whether a
	// review takes eight seconds or eighty.
	moveTime time.Duration
}

// StockfishOptions are the few UCI options worth setting on a small box.
type StockfishOptions struct {
	Path     string
	Threads  int
	HashMB   int
	MoveTime time.Duration
}

func (o StockfishOptions) withDefaults() StockfishOptions {
	if o.Path == "" {
		o.Path = "stockfish"
	}
	// One thread and a small hash on purpose. The box has two cores and 2GB,
	// and the api and Postgres are on it too. A review that is twice as fast
	// and starves the site is not faster.
	if o.Threads == 0 {
		o.Threads = 1
	}
	if o.HashMB == 0 {
		o.HashMB = 64
	}
	if o.MoveTime == 0 {
		o.MoveTime = 120 * time.Millisecond
	}
	return o
}

// NewStockfish starts the engine and waits for it to be ready.
func NewStockfish(opts StockfishOptions) (*Stockfish, error) {
	opts = opts.withDefaults()

	cmd := exec.Command(opts.Path)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", opts.Path, err)
	}

	s := &Stockfish{cmd: cmd, in: in, out: bufio.NewReader(out), moveTime: opts.MoveTime}

	if err := s.send("uci"); err != nil {
		return nil, err
	}
	if err := s.await("uciok"); err != nil {
		return nil, err
	}
	for _, opt := range []string{
		fmt.Sprintf("setoption name Threads value %d", opts.Threads),
		fmt.Sprintf("setoption name Hash value %d", opts.HashMB),
		// Multiple lines would let us show the second best move too, and cost
		// search quality at this time control for something no one asked for.
		"setoption name MultiPV value 1",
	} {
		if err := s.send(opt); err != nil {
			return nil, err
		}
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Stockfish) send(line string) error {
	_, err := io.WriteString(s.in, line+"\n")
	return err
}

// await reads until a line starts with the given word.
func (s *Stockfish) await(word string) error {
	for {
		line, err := s.out.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.HasPrefix(strings.TrimSpace(line), word) {
			return nil
		}
	}
}

func (s *Stockfish) ready() error {
	if err := s.send("isready"); err != nil {
		return err
	}
	return s.await("readyok")
}

// Analyse returns what the engine thinks of a position.
//
// The score is read from the last info line before bestmove rather than the
// first, because the engine reports deeper and deeper opinions as it searches
// and only the last one reflects the whole time it was given.
func (s *Stockfish) Analyse(ctx context.Context, fen string) (Analysis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Analysis{}, errors.New("engine is closed")
	}

	if err := s.send("position fen " + fen); err != nil {
		return Analysis{}, err
	}
	if err := s.send(fmt.Sprintf("go movetime %d", s.moveTime.Milliseconds())); err != nil {
		return Analysis{}, err
	}

	var a Analysis
	for {
		if err := ctx.Err(); err != nil {
			return Analysis{}, err
		}
		line, err := s.out.ReadString('\n')
		if err != nil {
			return Analysis{}, err
		}
		line = strings.TrimSpace(line)

		if cp, mate, ok := parseScore(line); ok {
			a.CP, a.Mate = cp, mate
		}
		if best, ok := strings.CutPrefix(line, "bestmove "); ok {
			a.Best = strings.Fields(best)[0]
			return a, nil
		}
	}
}

// parseScore pulls the evaluation out of an info line. Lines without a score,
// such as the currmove progress reports, are ignored.
func parseScore(line string) (cp, mate int, ok bool) {
	if !strings.HasPrefix(line, "info ") || !strings.Contains(line, " score ") {
		return 0, 0, false
	}
	f := strings.Fields(line)
	for i := 0; i+2 < len(f); i++ {
		if f[i] != "score" {
			continue
		}
		v, err := strconv.Atoi(f[i+2])
		if err != nil {
			return 0, 0, false
		}
		switch f[i+1] {
		case "cp":
			return v, 0, true
		case "mate":
			return 0, v, true
		}
	}
	return 0, 0, false
}

func (s *Stockfish) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = s.send("quit")
	// Give it a moment to go on its own before insisting, so it can write out
	// anything it wanted to.
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
		return <-done
	}
}
