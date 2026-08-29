package store

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// The statistics are the feature, so they are tested directly before anything
// touches a database.
func TestWilsonWidensAsTheSampleShrinks(t *testing.T) {
	smallLo, smallHi := wilson(1, 3)
	bigLo, bigHi := wilson(100, 300)
	// Both are one third. Only one of them is worth acting on.
	if (smallHi - smallLo) <= (bigHi - bigLo) {
		t.Errorf("1 of 3 spans %.3f and 100 of 300 spans %.3f; the small sample must be wider",
			smallHi-smallLo, bigHi-bigLo)
	}
}

// The textbook normal interval has zero width at 0 of n and claims certainty.
// Wilson must not, because a beginner missing their first eight forks is the
// exact case this feature exists for.
func TestWilsonDoesNotClaimCertaintyAtZero(t *testing.T) {
	lo, hi := wilson(0, 8)
	if hi <= 0 {
		t.Errorf("0 of 8 gave an upper bound of %.3f, claiming certainty", hi)
	}
	if lo < 0 || hi > 1 {
		t.Errorf("interval [%.3f, %.3f] leaves the range a proportion can take", lo, hi)
	}
	loAll, hiAll := wilson(8, 8)
	if loAll >= 1 || hiAll > 1.000001 {
		t.Errorf("8 of 8 gave [%.3f, %.3f]", loAll, hiAll)
	}
}

func TestWilsonCentresOnTheProportion(t *testing.T) {
	lo, hi := wilson(500, 1000)
	mid := (lo + hi) / 2
	if math.Abs(mid-0.5) > 0.01 {
		t.Errorf("a large even sample centred on %.3f, want about 0.5", mid)
	}
}

// End to end against the database: a player who is plainly worse at one theme
// is told so, and a player with three attempts is not.
func TestWeaknessNeedsEvidence(t *testing.T) {
	archive, users, ctx := testArchive(t)
	rooms := NewClassrooms(archive.Pool())
	workFixtures(t, rooms, ctx)

	// Two more forks, so a theme can reach the reporting threshold.
	if _, err := archive.Pool().Exec(ctx, `
		INSERT INTO puzzles (id, fen, moves, rating, solution_plies, phase, themes)
		VALUES
		  ('wkfork3','8/8/8/8/8/8/8/K6k w - - 0 1','a1a2',1300,1,'middlegame',ARRAY['fork']),
		  ('wkfork4','8/8/8/8/8/8/8/K6k w - - 0 1','a1a2',1300,1,'middlegame',ARRAY['fork']),
		  ('wkfork5','8/8/8/8/8/8/8/K6k w - - 0 1','a1a2',1300,1,'middlegame',ARRAY['fork']),
		  ('wkfork6','8/8/8/8/8/8/8/K6k w - - 0 1','a1a2',1300,1,'middlegame',ARRAY['fork']),
		  ('wkpin2','8/8/8/8/8/8/8/K6k w - - 0 1','a1a2',1300,1,'middlegame',ARRAY['pin']),
		  ('wkpin3','8/8/8/8/8/8/8/K6k w - - 0 1','a1a2',1300,1,'middlegame',ARRAY['pin'])
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}

	player, err := users.Create(ctx, "weak_player", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	rec := func(puzzle string, solved bool) {
		t.Helper()
		if _, err := archive.Pool().Exec(ctx, `
			INSERT INTO puzzle_attempts (user_id, puzzle_id, solved, attempted_at)
			VALUES ($1, $2, $3, now())`, player.ID, puzzle, solved); err != nil {
			t.Fatal(err)
		}
	}

	// Six forks, all missed, against a solid record on everything else. Two
	// pins is deliberately too few to say anything about pins.
	for _, p := range []string{"workfork1", "workfork2", "wkfork3", "wkfork4", "wkfork5", "wkfork6"} {
		rec(p, false)
	}
	rec("workpin1", true)
	rec("wkpin2", true)
	// The baseline: eight other puzzles, solved. Without these the player has
	// no history apart from forks, and there is nothing to compare against.
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("wkother%d", i)
		if _, err := archive.Pool().Exec(ctx, `
			INSERT INTO puzzles (id, fen, moves, rating, solution_plies, phase, themes)
			VALUES ($1,'8/8/8/8/8/8/8/K6k w - - 0 1','a1a2',1300,1,'middlegame',ARRAY['promotion'])
			ON CONFLICT (id) DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		rec(id, true)
	}

	got, err := archive.Weaknesses(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}

	byTheme := map[string]ThemeRecord{}
	for _, r := range got.Themes {
		byTheme[r.Theme] = r
	}
	fork, ok := byTheme["fork"]
	if !ok {
		t.Fatalf("fork was not reported at all; got %+v", got.Themes)
	}
	if fork.Verdict != Weak {
		t.Errorf("six forks all missed was judged %q", fork.Verdict)
	}
	// Two attempts is under the threshold, so pin must not appear at all.
	if _, reported := byTheme["pin"]; reported {
		t.Errorf("pin was reported on two attempts: %+v", byTheme["pin"])
	}
}

// The same puzzle attempted repeatedly must not stack up as evidence.
func TestOnlyTheFirstTryCounts(t *testing.T) {
	archive, users, ctx := testArchive(t)
	rooms := NewClassrooms(archive.Pool())
	workFixtures(t, rooms, ctx)

	player, err := users.Create(ctx, "retry_player", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	// Missed first, then solved on the fourth go. The honest record is a miss.
	base := time.Now().Add(-time.Hour)
	for i, solved := range []bool{false, false, false, true} {
		if _, err := archive.Pool().Exec(ctx, `
			INSERT INTO puzzle_attempts (user_id, puzzle_id, solved, attempted_at)
			VALUES ($1, 'workfork1', $2, $3)`,
			player.ID, solved, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := archive.Weaknesses(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 1 {
		t.Errorf("four attempts at one puzzle counted as %d, want 1", got.Attempts)
	}
	if got.Solved != 0 {
		t.Errorf("a puzzle missed first and solved later counted as solved")
	}
}

func TestNothingToSayWithNoAttempts(t *testing.T) {
	archive, users, ctx := testArchive(t)
	player, err := users.Create(ctx, "fresh_player", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	got, err := archive.Weaknesses(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 0 || len(got.Themes) != 0 {
		t.Errorf("a new account got %+v", got)
	}
}
