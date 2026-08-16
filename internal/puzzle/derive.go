// Package puzzle turns a row of the Lichess CC0 puzzle dump into the shape
// this site serves, and holds the pieces of that shape that are worth testing
// on their own.
//
// Everything here is a pure function of one CSV row. The filters the search
// runs on (length, phase, theme) are derived once at load time and stored,
// because a filter computed per query is a filter no index can serve.
package puzzle

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Puzzle is one row after derivation, ready to be written.
type Puzzle struct {
	ID            string
	FEN           string
	Moves         []string
	Rating        float64
	RatingDev     float64
	Popularity    int
	NbPlays       int
	Themes        []string
	GameURL       string
	OpeningTags   []string
	SolutionPlies int
	Phase         string
}

const (
	PhaseOpening    = "opening"
	PhaseMiddlegame = "middlegame"
	PhaseEndgame    = "endgame"
)

// Header is the column order of lichess_db_puzzle.csv. A DailyDate column was
// appended after the file was first published, so the parser checks the
// prefix rather than the whole row: a new trailing column should not stop the
// load, but a reordered one must.
var Header = []string{
	"PuzzleId", "FEN", "Moves", "Rating", "RatingDeviation",
	"Popularity", "NbPlays", "Themes", "GameUrl", "OpeningTags",
}

// CheckHeader verifies a parsed CSV header line against the columns this
// parser reads.
func CheckHeader(cols []string) error {
	if len(cols) < len(Header) {
		return fmt.Errorf("header has %d columns, need at least %d", len(cols), len(Header))
	}
	for i, want := range Header {
		if cols[i] != want {
			return fmt.Errorf("column %d is %q, want %q", i, cols[i], want)
		}
	}
	return nil
}

// Parse turns one CSV record into a Puzzle.
func Parse(rec []string) (Puzzle, error) {
	if len(rec) < len(Header) {
		return Puzzle{}, fmt.Errorf("record has %d columns, need at least %d", len(rec), len(Header))
	}
	p := Puzzle{
		ID:          rec[0],
		FEN:         rec[1],
		Moves:       strings.Fields(rec[2]),
		Themes:      normalizeTags(rec[7]),
		GameURL:     rec[8],
		OpeningTags: normalizeTags(rec[9]),
	}
	if p.ID == "" {
		return Puzzle{}, fmt.Errorf("empty puzzle id")
	}
	var err error
	if p.Rating, err = strconv.ParseFloat(rec[3], 64); err != nil {
		return Puzzle{}, fmt.Errorf("rating: %w", err)
	}
	if p.RatingDev, err = strconv.ParseFloat(rec[4], 64); err != nil {
		return Puzzle{}, fmt.Errorf("rating deviation: %w", err)
	}
	if p.Popularity, err = strconv.Atoi(rec[5]); err != nil {
		return Puzzle{}, fmt.Errorf("popularity: %w", err)
	}
	if p.NbPlays, err = strconv.Atoi(rec[6]); err != nil {
		return Puzzle{}, fmt.Errorf("plays: %w", err)
	}

	// The first move is the opponent's blunder, played for you before you are
	// asked anything, so it is not part of the solution.
	if len(p.Moves) < 2 {
		return Puzzle{}, fmt.Errorf("puzzle %s has %d moves, need at least 2", p.ID, len(p.Moves))
	}
	p.SolutionPlies = len(p.Moves) - 1
	p.Phase = phase(p.Themes, p.FEN)
	return p, nil
}

// UserMoves is how many moves the solver actually plays, which is the number
// the length filter is written in. The plies alternate and always end on the
// solver, so a five ply solution is three moves.
func (p Puzzle) UserMoves() int { return (p.SolutionPlies + 1) / 2 }

// SetupMove is the opponent's blunder, played automatically before the solver
// is asked anything.
func (p Puzzle) SetupMove() string {
	if len(p.Moves) == 0 {
		return ""
	}
	return p.Moves[0]
}

// Solution is everything after the blunder: the solver's moves and the
// opponent's replies, alternating, ending on the solver.
func (p Puzzle) Solution() []string {
	if len(p.Moves) < 2 {
		return nil
	}
	return p.Moves[1:]
}

// SolverColor is the side the player takes. The FEN is the position before the
// blunder, so the side to move there is the opponent and the solver is the
// other one.
func (p Puzzle) SolverColor() string {
	fields := strings.Fields(p.FEN)
	if len(fields) >= 2 && fields[1] == "w" {
		return "black"
	}
	return "white"
}

// PliesFor turns a length in solver moves into the ply count stored on the
// row. Solutions always end on the solver, so they are always odd.
func PliesFor(moves int) int { return moves*2 - 1 }

// normalizeTags splits a space separated tag list, drops duplicates and sorts
// it. Sorting is for stable output rather than for the index: a GIN index on
// an array does not care about order, but a diff of two loads does.
func normalizeTags(s string) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// phase prefers the tag Lichess already assigned, because theirs comes from
// the whole game and this only ever sees one position. The fallback matters
// for puzzles generated here later, and for the small share of theirs that
// arrive with no phase tag at all.
func phase(themes []string, fen string) string {
	for _, t := range themes {
		switch t {
		case PhaseOpening, PhaseMiddlegame, PhaseEndgame:
			return t
		}
	}
	return phaseFromFEN(fen)
}

// phaseFromFEN counts majors and minors, which is how Lichess's own divider
// decides. Six or fewer left on the board is an endgame there and there is no
// reason to invent a different threshold. Before that, the move number
// separates an opening from a middlegame; twelve is arbitrary but is roughly
// where an opening book stops being the reason for the moves.
//
// This reads the FEN as a string rather than through a chess library. It runs
// six million times on a load and needs a piece count, not a legal position.
func phaseFromFEN(fen string) string {
	board, rest, _ := strings.Cut(fen, " ")
	majorsAndMinors := 0
	for _, c := range board {
		switch c {
		case 'q', 'r', 'b', 'n', 'Q', 'R', 'B', 'N':
			majorsAndMinors++
		}
	}
	if majorsAndMinors <= 6 {
		return PhaseEndgame
	}
	// Fields are side, castling, en passant, halfmove clock, fullmove number.
	fields := strings.Fields(rest)
	if len(fields) >= 5 {
		if n, err := strconv.Atoi(fields[4]); err == nil && n <= 12 {
			return PhaseOpening
		}
	}
	return PhaseMiddlegame
}
