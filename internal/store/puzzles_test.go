package store

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/leozh0u/blundernet/internal/puzzle"
)

// These run against a real Postgres because what is being tested is the SQL
// and the sampler on top of it, neither of which a fake would exercise.
// TEST_DATABASE_URL points at one; CI starts a service container.
func testPuzzles(t *testing.T) (*Puzzles, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	archive, err := NewArchive(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(archive.Close)
	if _, err := archive.Pool().Exec(ctx, "TRUNCATE puzzles CASCADE"); err != nil {
		t.Fatal(err)
	}
	return NewPuzzles(archive.Pool()), ctx
}

// sliceSource feeds a slice of puzzles to the loader.
type sliceSource struct {
	rows []puzzle.Puzzle
	i    int
}

func (s *sliceSource) Next() bool            { s.i++; return s.i <= len(s.rows) }
func (s *sliceSource) Puzzle() puzzle.Puzzle { return s.rows[s.i-1] }
func (s *sliceSource) Err() error            { return nil }

// fixture builds puzzles spread across two rating bands, two phases and two
// lengths, with the three ply cells ten times the size of the five ply ones so
// the weighting has something to get wrong.
func fixture() []puzzle.Puzzle {
	var out []puzzle.Puzzle
	add := func(n int, rating float64, phase string, plies int, themes []string) {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s%d%d%d", phase[:3], int(rating), plies, i)
			out = append(out, puzzle.Puzzle{
				ID: id, FEN: "8/8/8/8/8/8/8/K6k w - - 0 1",
				Moves:      []string{"a1a2", "h1h2", "a2a3", "h2h3", "a3a4", "h3h4"}[:plies+1],
				Rating:     rating,
				RatingDev:  75,
				Popularity: 90,
				Themes:     themes,
				// SolutionPlies is normally derived by the parser; the loader
				// takes whatever it is given, so set it explicitly here.
				SolutionPlies: plies,
				Phase:         phase,
			})
		}
	}
	add(500, 1450, puzzle.PhaseMiddlegame, 3, []string{"fork"})
	add(500, 1550, puzzle.PhaseMiddlegame, 3, []string{"pin"})
	add(50, 1450, puzzle.PhaseEndgame, 5, []string{"fork"})
	add(50, 1550, puzzle.PhaseEndgame, 5, []string{"skewer"})
	return out
}

func load(t *testing.T, p *Puzzles, ctx context.Context, rows []puzzle.Puzzle) {
	t.Helper()
	copied, merged, err := p.Load(ctx, &sliceSource{rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	if int(copied) != len(rows) || int(merged) != len(rows) {
		t.Fatalf("copied %d merged %d, want %d of each", copied, merged, len(rows))
	}
	if err := p.RefreshCells(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Analyze(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIsIdempotent(t *testing.T) {
	p, ctx := testPuzzles(t)
	rows := fixture()
	load(t, p, ctx, rows)
	// A second load of the same dump must update in place rather than
	// duplicate or fail, because refreshing ratings monthly is the point.
	load(t, p, ctx, rows)

	got, err := p.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if int(got) != len(rows) {
		t.Errorf("count = %d after two loads, want %d", got, len(rows))
	}
}

func TestSelectRespectsFilters(t *testing.T) {
	p, ctx := testPuzzles(t)
	load(t, p, ctx, fixture())

	f := Filter{
		MinRating: 1500, MaxRating: 1599,
		Phases:   []string{puzzle.PhaseEndgame},
		MinPlies: 5, MaxPlies: 5,
		Themes:        []string{"skewer"},
		MinPopularity: 80,
	}
	got, err := p.Select(ctx, f, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d puzzles, want 10", len(got))
	}
	for _, q := range got {
		if q.Rating < 1500 || q.Rating > 1599 || q.Phase != puzzle.PhaseEndgame ||
			q.SolutionPlies != 5 || !contains(q.Themes, "skewer") {
			t.Errorf("puzzle %s does not match the filter: %+v", q.ID, q)
		}
	}
}

func TestSelectReturnsNothingWhenNothingMatches(t *testing.T) {
	p, ctx := testPuzzles(t)
	load(t, p, ctx, fixture())

	got, err := p.Select(ctx, Filter{MinRating: 2800, MaxRating: 2900}, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d puzzles for an empty rating band, want none", len(got))
	}
}

func TestSelectSkipsSeen(t *testing.T) {
	p, ctx := testPuzzles(t)
	load(t, p, ctx, fixture())

	first, err := p.Select(ctx, Filter{}, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, q := range first {
		seen[q.ID] = true
	}
	next, err := p.Select(ctx, Filter{}, 20, func(id string) bool { return seen[id] })
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range next {
		if seen[q.ID] {
			t.Errorf("puzzle %s came back after being marked seen", q.ID)
		}
	}
}

// The sampler scans forward from a random cursor, so without the wrapped scan
// the puzzles near the end of a cell's shuffle would be unreachable. Draining
// a small cell proves every row can be reached.
func TestSelectReachesEveryPuzzleInACell(t *testing.T) {
	p, ctx := testPuzzles(t)
	load(t, p, ctx, fixture())

	f := Filter{
		MinRating: 1500, MaxRating: 1599,
		Phases: []string{puzzle.PhaseEndgame}, MinPlies: 5, MaxPlies: 5,
	}
	seen := map[string]bool{}
	for round := 0; round < 60 && len(seen) < 50; round++ {
		got, err := p.Select(ctx, f, 25, func(id string) bool { return seen[id] })
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range got {
			seen[q.ID] = true
		}
	}
	if len(seen) != 50 {
		t.Errorf("reached %d of 50 puzzles in the cell", len(seen))
	}
}

// Cells are drawn in proportion to their size, so a filter spanning a cell of
// 1000 and a cell of 100 must return roughly ten times as many from the first.
// Uniform-over-cells would give an even split and fail this.
func TestSelectWeightsCellsBySize(t *testing.T) {
	p, ctx := testPuzzles(t)
	load(t, p, ctx, fixture())

	short, long := 0, 0
	for i := 0; i < 40; i++ {
		got, err := p.Select(ctx, Filter{}, 25, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range got {
			if q.SolutionPlies == 3 {
				short++
			} else {
				long++
			}
		}
	}
	total := short + long
	if total == 0 {
		t.Fatal("no puzzles returned")
	}
	// The population is 1000 three ply to 100 five ply, so about 91%. The
	// bound is wide because this is a sampler, not a fixed sequence.
	ratio := float64(short) / float64(total)
	if ratio < 0.80 || ratio > 0.98 {
		t.Errorf("three ply share = %.2f (%d of %d), want near 0.91", ratio, short, total)
	}
}

func TestByID(t *testing.T) {
	p, ctx := testPuzzles(t)
	rows := fixture()
	load(t, p, ctx, rows)

	want := rows[0]
	got, ok, err := p.ByID(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("puzzle %s not found", want.ID)
	}
	if got.ID != want.ID || got.FEN != want.FEN || len(got.Moves) != len(want.Moves) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, ok, err := p.ByID(ctx, "nosuchpuzzle"); err != nil || ok {
		t.Errorf("a missing puzzle should report not found, got ok=%v err=%v", ok, err)
	}
}

// BenchmarkSelect runs against a database that already holds the real import,
// because the numbers only mean something at six million rows. It never
// writes, so it points at its own variable rather than the one the tests
// truncate.
//
//	PUZZLE_DATABASE_URL=... go test ./internal/store -bench Select -run x
func BenchmarkSelect(b *testing.B) {
	url := os.Getenv("PUZZLE_DATABASE_URL")
	if url == "" {
		b.Skip("PUZZLE_DATABASE_URL is not set")
	}
	ctx := context.Background()
	archive, err := NewArchive(ctx, url)
	if err != nil {
		b.Fatal(err)
	}
	defer archive.Close()
	p := NewPuzzles(archive.Pool())

	cases := []struct {
		name string
		f    Filter
	}{
		{"rating only", Filter{MinRating: 1400, MaxRating: 1600}},
		{"rating and phase", Filter{MinRating: 1400, MaxRating: 1600, Phases: []string{puzzle.PhaseEndgame}}},
		{"common theme", Filter{MinRating: 1400, MaxRating: 1600, MinPlies: 3, MaxPlies: 3, Themes: []string{"fork"}}},
		{"rare theme", Filter{MinRating: 2000, MaxRating: 2400, Themes: []string{"smotheredMate"}}},
		{"everything", Filter{}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				got, err := p.Select(ctx, c.f, 20, nil)
				if err != nil {
					b.Fatal(err)
				}
				if len(got) == 0 {
					b.Fatal("no puzzles returned")
				}
			}
		})
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
