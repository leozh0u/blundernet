package puzzle

import (
	"reflect"
	"testing"
)

// Real rows from lichess_db_puzzle.csv, trimmed to the columns read here.
var (
	rowMiddlegame = []string{
		"00008", "r6k/pp2r2p/4Rp1Q/3p4/8/1N1P2R1/PqP2bPP/7K b - - 0 24",
		"f2g3 e6e7 b2b1 b3c1 b1c1 h6c1", "1939", "77", "95", "10000",
		"crushing hangingPiece long middlegame", "https://lichess.org/787zsVup/black#48", "",
	}
	rowEndgame = []string{
		"0000D", "5rk1/1p3ppp/pq3b2/8/8/1P1Q1N2/P4PPP/3R2K1 w - - 2 27",
		"d3d6 f8d8 d6d8 f6d8", "1559", "74", "96", "37137",
		"advantage endgame short", "https://lichess.org/F8M8OS71#53", "",
	}
)

func TestParse(t *testing.T) {
	p, err := Parse(rowMiddlegame)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "00008" || p.Rating != 1939 || p.RatingDev != 77 {
		t.Errorf("got %+v", p)
	}
	if p.Popularity != 95 || p.NbPlays != 10000 {
		t.Errorf("popularity %d plays %d", p.Popularity, p.NbPlays)
	}
	// Six moves in the file, the first of which is the blunder being punished.
	if p.SolutionPlies != 5 {
		t.Errorf("solution plies = %d, want 5", p.SolutionPlies)
	}
	if p.UserMoves() != 3 {
		t.Errorf("user moves = %d, want 3", p.UserMoves())
	}
	if p.Phase != PhaseMiddlegame {
		t.Errorf("phase = %q", p.Phase)
	}
	want := []string{"crushing", "hangingPiece", "long", "middlegame"}
	if !reflect.DeepEqual(p.Themes, want) {
		t.Errorf("themes = %v, want %v", p.Themes, want)
	}
	if p.OpeningTags != nil {
		t.Errorf("opening tags = %v, want none", p.OpeningTags)
	}
}

func TestParseRejectsShortSolutions(t *testing.T) {
	// One move is the blunder with nothing to answer it, which is not a puzzle.
	rec := append([]string(nil), rowEndgame...)
	rec[2] = "d3d6"
	if _, err := Parse(rec); err == nil {
		t.Fatal("expected an error for a one move puzzle")
	}
}

func TestParseRejectsBadNumbers(t *testing.T) {
	rec := append([]string(nil), rowEndgame...)
	rec[3] = "unrated"
	if _, err := Parse(rec); err == nil {
		t.Fatal("expected an error for a non-numeric rating")
	}
}

func TestPhasePrefersTheTag(t *testing.T) {
	// This position has eight majors and minors and is on move 27, so the FEN
	// alone would say middlegame. Lichess tagged it an endgame from the whole
	// game and their tag wins.
	p, err := Parse(rowEndgame)
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != PhaseEndgame {
		t.Errorf("phase = %q, want %q", p.Phase, PhaseEndgame)
	}
}

func TestPhaseFromFEN(t *testing.T) {
	cases := []struct {
		name string
		fen  string
		want string
	}{
		{"start", "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1", PhaseOpening},
		{"move 30 with a full board", "r1bqk2r/pppp1ppp/2n2n2/2b1p3/2B1P3/2N2N2/PPPP1PPP/R1BQK2R w KQkq - 0 30", PhaseMiddlegame},
		{"rook and pawns", "8/4R3/1p2P3/p4r2/P6p/1P3Pk1/4K3/8 w - - 1 64", PhaseEndgame},
		{"kings and pawns", "8/5k2/8/4p3/4P3/8/5K2/8 w - - 0 50", PhaseEndgame},
		{"truncated fen falls through to middlegame", "r1bqk2r/pppp1ppp/2n2n2/2b1p3/2B1P3/2N2N2/PPPP1PPP/R1BQK2R w", PhaseMiddlegame},
	}
	for _, c := range cases {
		if got := phaseFromFEN(c.fen); got != c.want {
			t.Errorf("%s: phase = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNormalizeTags(t *testing.T) {
	got := normalizeTags("  fork  pin fork  ")
	want := []string{"fork", "pin"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if normalizeTags("") != nil {
		t.Error("empty tag list should be nil, not an empty slice")
	}
}

func TestCheckHeader(t *testing.T) {
	full := append(append([]string(nil), Header...), "DailyDate")
	if err := CheckHeader(full); err != nil {
		t.Errorf("a trailing new column should be accepted: %v", err)
	}
	swapped := append([]string(nil), Header...)
	swapped[1], swapped[2] = swapped[2], swapped[1]
	if err := CheckHeader(swapped); err == nil {
		t.Error("reordered columns should be rejected")
	}
	if err := CheckHeader(Header[:3]); err == nil {
		t.Error("a short header should be rejected")
	}
}
