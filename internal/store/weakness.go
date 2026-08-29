package store

import (
	"context"
	"math"
)

// What a player is actually bad at.
//
// The naive version of this feature counts solve rates per theme and shows a
// list. It is wrong in two ways that matter, and both are about honesty rather
// than arithmetic.
//
// First, what is a bad rate? Forty percent on skewers means nothing on its
// own, because puzzles vary in difficulty and so do players. It only means
// something against how that same player does on everything else. So the
// comparison is always self-relative.
//
// And "everything else" has to mean everything else. Comparing skewers against
// an overall rate that includes the skewers compares them partly against
// themselves, which flattens exactly the case this is for: somebody who has
// drilled mostly one theme and is bad at it drags their own average down until
// the theme looks average. The baseline for each theme is therefore the rate on
// puzzles not carrying that theme.
//
// Second, and this is the one people get wrong: three attempts is not
// evidence. One in three looks like 33% and is entirely consistent with being
// fine at skewers and unlucky. Saying "you are weak at skewers" on that is
// worse than saying nothing, because a beginner will believe it and go and
// drill the wrong thing. So a theme is only reported when the statistics can
// carry it, using a Wilson interval rather than a raw proportion.

// A theme needs at least this many attempts before it can be talked about at
// all. The interval below would refuse most small samples anyway; this stops
// the query returning a list of ones and twos to filter through.
const minAttemptsPerTheme = 6

// The comparison also needs something to compare against. A player whose whole
// history is forks has no baseline, and inventing one from four other puzzles
// would produce a confident claim out of nothing.
const minBaselineAttempts = 6

// The tactical themes worth reporting on.
//
// The corpus carries 73 tags and most of the tail is not a tactic: phases
// (opening, endgame), lengths (short, long), and notes about the source game
// (master, superGM). Telling somebody they are weak at "short" is noise. This
// mirrors the list the puzzle filter offers, because those are the things
// somebody can go and practise.
var reportableThemes = []string{
	"fork", "pin", "skewer", "discoveredAttack", "doubleCheck", "deflection",
	"attraction", "clearance", "interference", "sacrifice", "hangingPiece",
	"trappedPiece", "backRankMate", "smotheredMate", "promotion",
	"underPromotion", "zugzwang", "quietMove", "defensiveMove", "intermezzo",
	"xRayAttack", "capturingDefender", "mateIn1", "mateIn2", "mateIn3",
}

// Verdict is what can honestly be said about one theme.
type Verdict string

const (
	// Weak and Strong mean the interval clears the player's own overall rate.
	Weak   Verdict = "weak"
	Strong Verdict = "strong"
	// Unclear means there is not enough evidence to say either way, which is a
	// real answer and the most common one.
	Unclear Verdict = "unclear"
)

type ThemeRecord struct {
	Theme    string  `json:"theme"`
	Attempts int     `json:"attempts"`
	Solved   int     `json:"solved"`
	Rate     float64 `json:"rate"`
	// Baseline is the same player's rate on everything that is not this theme,
	// which is what the verdict compares against.
	Baseline float64 `json:"baseline"`
	Verdict  Verdict `json:"verdict"`
}

type Weakness struct {
	// Overall is the rate everything else is measured against.
	Attempts int           `json:"attempts"`
	Solved   int           `json:"solved"`
	Rate     float64       `json:"rate"`
	Themes   []ThemeRecord `json:"themes"`
}

// wilson returns the lower and upper bound of a 95% confidence interval for a
// proportion.
//
// A raw proportion has no idea how much data is behind it: 1 of 3 and 100 of
// 300 are both "33%" and only one of them is worth acting on. The Wilson
// interval widens as the sample shrinks, so small samples produce bounds so
// wide that they overlap the overall rate and the theme is reported as
// unclear, which is exactly the behaviour wanted.
//
// Wilson rather than the textbook normal interval because it stays sensible
// near zero and one, where a beginner's numbers actually live: at 0 of 8 the
// normal interval has zero width and claims certainty.
func wilson(solved, attempts int) (lo, hi float64) {
	if attempts == 0 {
		return 0, 1
	}
	const z = 1.96 // 95%
	n := float64(attempts)
	p := float64(solved) / n
	denom := 1 + z*z/n
	centre := (p + z*z/(2*n)) / denom
	spread := (z / denom) * math.Sqrt(p*(1-p)/n+z*z/(4*n*n))
	return centre - spread, centre + spread
}

// Weaknesses reports where a player is measurably above or below their own
// average, and says nothing where the numbers cannot support a claim.
func (a *Archive) Weaknesses(ctx context.Context, userID string) (*Weakness, error) {
	// Each puzzle counts once, on the first attempt.
	//
	// Counting every attempt would let one puzzle retried five times dominate
	// a theme, and counting the best attempt would measure persistence rather
	// than skill. The honest question is "could you solve this cold", and that
	// is the first try.
	const firstTries = `
		WITH first_try AS (
		    SELECT DISTINCT ON (puzzle_id) puzzle_id, solved
		    FROM puzzle_attempts
		    WHERE user_id = $1
		    ORDER BY puzzle_id, attempted_at
		)`

	out := &Weakness{Themes: []ThemeRecord{}}
	err := a.pool.QueryRow(ctx, firstTries+`
		SELECT count(*), count(*) FILTER (WHERE solved) FROM first_try`, userID).
		Scan(&out.Attempts, &out.Solved)
	if err != nil {
		return nil, err
	}
	if out.Attempts == 0 {
		return out, nil
	}
	out.Rate = float64(out.Solved) / float64(out.Attempts)

	// unnest turns the themes array into rows, so one puzzle tagged fork and
	// hangingPiece counts towards both, which is correct: it is a fork and it
	// is a hanging piece, and failing it is evidence about each.
	rows, err := a.pool.Query(ctx, firstTries+`
		SELECT t.theme, count(*), count(*) FILTER (WHERE f.solved)
		FROM first_try f
		JOIN puzzles p ON p.id = f.puzzle_id
		CROSS JOIN LATERAL unnest(p.themes) AS t(theme)
		WHERE t.theme = ANY ($2)
		GROUP BY t.theme
		HAVING count(*) >= $3
		ORDER BY t.theme`, userID, reportableThemes, minAttemptsPerTheme)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var r ThemeRecord
		if err := rows.Scan(&r.Theme, &r.Attempts, &r.Solved); err != nil {
			return nil, err
		}
		r.Rate = float64(r.Solved) / float64(r.Attempts)

		// Everything that is not this theme. A puzzle carrying two reported
		// themes is excluded from both baselines, which is right: it cannot be
		// evidence about a theme and a control for it at the same time.
		otherAttempts := out.Attempts - r.Attempts
		otherSolved := out.Solved - r.Solved
		if otherAttempts < minBaselineAttempts {
			r.Verdict = Unclear
			r.Baseline = out.Rate
			out.Themes = append(out.Themes, r)
			continue
		}
		r.Baseline = float64(otherSolved) / float64(otherAttempts)

		lo, hi := wilson(r.Solved, r.Attempts)
		switch {
		case hi < r.Baseline:
			r.Verdict = Weak // confidently below the rest of your own play
		case lo > r.Baseline:
			r.Verdict = Strong
		default:
			r.Verdict = Unclear
		}
		out.Themes = append(out.Themes, r)
	}
	return out, rows.Err()
}
