package review

import (
	"errors"
	"strings"
	"testing"
)

// What people actually paste, which is never a clean PGN file.
func TestParsesWhatPeopleActuallyPaste(t *testing.T) {
	cases := []struct {
		name string
		pgn  string
	}{
		{"bare moves", "1. e4 e5 2. Bc4 Nc6 3. Qh5 Nf6 4. Qxf7#"},
		{"no move numbers", "e4 e5 Bc4 Nc6 Qh5 Nf6 Qxf7#"},
		{"with a result", "1. e4 e5 2. Bc4 Nc6 3. Qh5 Nf6 4. Qxf7# 1-0"},
		{"black ellipsis", "1. e4 1... e5 2. Bc4 2... Nc6 3. Qh5 3... Nf6 4. Qxf7#"},
		{
			"lichess export with headers and clocks",
			`[Event "Rated blitz game"]
[Site "https://lichess.org/abcd1234"]
[White "someone"]
[Black "someoneelse"]
[Result "1-0"]

1. e4 { [%clk 0:03:00] } 1... e5 { [%clk 0:03:00] } 2. Bc4 { [%clk 0:02:58] }
2... Nc6 { [%clk 0:02:57] } 3. Qh5 { [%clk 0:02:55] } 3... Nf6 { [%clk 0:02:50] }
4. Qxf7# { [%clk 0:02:49] } 1-0`,
		},
		{"annotations", "1. e4 e5 2. Bc4!? Nc6 3. Qh5?! Nf6?? 4. Qxf7#"},
		{"a variation nobody wants", "1. e4 e5 2. Bc4 (2. Nf3 Nc6 3. Bb5) 2... Nc6 3. Qh5 Nf6 4. Qxf7#"},
	}

	want := []string{"e2e4", "e7e5", "f1c4", "b8c6", "d1h5", "g8f6", "h5f7"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePGN(c.pgn)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d moves %v, want %d", len(got), got, len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("move %d is %s, want %s", i+1, got[i], want[i])
				}
			}
		})
	}
}

func TestRefusesWhatIsNotAGame(t *testing.T) {
	for _, s := range []string{"", "   ", "hello there", "[Event \"x\"]"} {
		if _, err := ParsePGN(s); !errors.Is(err, ErrNoMoves) {
			t.Errorf("ParsePGN(%q) gave %v, want ErrNoMoves", s, err)
		}
	}
}

func TestRefusesAGameTooLongToReview(t *testing.T) {
	// A legal shuffle, repeated well past the cap.
	var b strings.Builder
	b.WriteString("1. Nf3 Nf6 2. Ng1 Ng8 ")
	for i := 3; i < 200; i += 2 {
		b.WriteString("Nf3 Nf6 Ng1 Ng8 ")
	}
	if _, err := ParsePGN(b.String()); !errors.Is(err, ErrTooLong) {
		t.Errorf("a very long game gave %v, want ErrTooLong", err)
	}
}

// A game that stops making sense partway is still worth what came before it.
func TestKeepsWhatItCanRead(t *testing.T) {
	got, err := ParsePGN("1. e4 e5 2. Bc4 Qxq9 Nc6")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("got %v, want the four legal moves around the nonsense", got)
	}
}
