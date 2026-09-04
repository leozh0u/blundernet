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

// Line is one candidate move and what the engine thinks of playing it.
type Line struct {
	// CP and Mate are scored from the point of view of the side to move, the
	// same way Analysis is.
	CP   int
	Mate int
	// Move is the first move of the line, in UCI.
	Move string
}

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
	// Second is the engine's runner up, present only when MultiPV is above one
	// and the position has more than one legal move.
	//
	// It exists to answer "was there another way", which is the difference
	// between a move that was good and a move that was the only one. Nothing
	// else in the review can tell those apart.
	Second *Line
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
	// broken is set when a read timed out. The pipe is then mid-answer and
	// there is no safe way to resynchronise, so every later call fails fast
	// rather than reading somebody else's reply.
	broken bool
}

// SetMoveTime changes the per position budget. Callers that know how many
// positions they are about to ask about use this to keep the whole job inside
// a deadline, rather than discovering halfway through that they are over it.
func (s *Stockfish) SetMoveTime(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d > 0 {
		s.moveTime = d
	}
}

// StockfishOptions are the few UCI options worth setting on a small box.
type StockfishOptions struct {
	Path     string
	Threads  int
	HashMB   int
	MoveTime time.Duration
	// MultiPV is how many candidate moves the engine reports. Two is what the
	// review wants; one is enough for anything that only needs a move.
	MultiPV int
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
	if o.MultiPV == 0 {
		o.MultiPV = 2
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
		// Two lines rather than one. The runner up is what makes "this was the
		// only move" a claim the review can actually check, and the cost is a
		// little depth on a search that is already only judging moves rather
		// than playing them.
		fmt.Sprintf("setoption name MultiPV value %d", opts.MultiPV),
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
	if s.broken {
		return Analysis{}, errors.New("engine is out of sync")
	}

	if err := s.send("position fen " + fen); err != nil {
		return Analysis{}, err
	}
	if err := s.send(fmt.Sprintf("go movetime %d", s.moveTime.Milliseconds())); err != nil {
		return Analysis{}, err
	}

	// The answer is read on another goroutine so this one can give up.
	//
	// Without that, a Stockfish that stops talking blocks the read forever
	// while holding the lock, and every review after it queues behind a
	// process that is never going to reply. A hung engine should take out one
	// job, not the worker.
	type result struct {
		a   Analysis
		err error
	}
	done := make(chan result, 1)
	go func() {
		// Keyed by MultiPV rank rather than appended, because the engine
		// reports each rank again at every new depth and only the last report
		// of each is its final opinion.
		lines := map[int]Line{}
		for {
			line, err := s.out.ReadString('\n')
			if err != nil {
				done <- result{err: err}
				return
			}
			line = strings.TrimSpace(line)
			if rank, l, ok := parseLine(line); ok {
				lines[rank] = l
			}
			if best, ok := strings.CutPrefix(line, "bestmove "); ok {
				var a Analysis
				a.Best = strings.Fields(best)[0]
				if first, ok := lines[1]; ok {
					a.CP, a.Mate = first.CP, first.Mate
				}
				// Only trust the runner up if it is a different move. A short
				// search can report the same move twice as it re-sorts.
				if second, ok := lines[2]; ok && second.Move != "" && second.Move != a.Best {
					s := second
					a.Second = &s
				}
				done <- result{a: a}
				return
			}
		}
	}()

	// Generous against the move time, since this is a stuck engine detector
	// and not a second time limit: Stockfish is already told how long to
	// think, so anything beyond this margin means it is not coming back.
	limit := 10*s.moveTime + 5*time.Second
	select {
	case r := <-done:
		return r.a, r.err
	case <-ctx.Done():
		s.broken = true
		return Analysis{}, ctx.Err()
	case <-time.After(limit):
		s.broken = true
		return Analysis{}, fmt.Errorf("engine did not answer within %s", limit)
	}
}

// parseLine pulls one candidate out of an info line: which rank it is, what it
// scores, and the move it starts with. Lines without a score, such as the
// currmove progress reports, are ignored.
//
// The rank defaults to 1 because an engine running with MultiPV 1 does not
// print a multipv field at all.
func parseLine(line string) (rank int, l Line, ok bool) {
	if !strings.HasPrefix(line, "info ") || !strings.Contains(line, " score ") {
		return 0, Line{}, false
	}
	f := strings.Fields(line)
	rank = 1
	for i := 0; i < len(f); i++ {
		switch {
		case f[i] == "multipv" && i+1 < len(f):
			if v, err := strconv.Atoi(f[i+1]); err == nil {
				rank = v
			}
		case f[i] == "score" && i+2 < len(f):
			v, err := strconv.Atoi(f[i+2])
			if err != nil {
				return 0, Line{}, false
			}
			switch f[i+1] {
			case "cp":
				l.CP, l.Mate = v, 0
				ok = true
			case "mate":
				l.CP, l.Mate = 0, v
				ok = true
			}
		case f[i] == "pv" && i+1 < len(f):
			// The line it intends to play; only the first move matters here.
			l.Move = f[i+1]
		}
	}
	if !ok {
		return 0, Line{}, false
	}
	return rank, l, true
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
