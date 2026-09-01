package rating

import (
	"math"
	"testing"
)

// The worked example from Glickman's paper: a 1500 player with deviation 200
// and volatility 0.06 plays three games, beating a 1400 (RD 30), losing to a
// 1550 (RD 100) and losing to a 1700 (RD 300). The paper gives the answer as
// rating 1464.06, deviation 151.52, volatility 0.05999.
//
// Pinning against a published result is the only way to know the
// implementation is right. Every intermediate step here is plausible-looking
// arithmetic that would fail silently if a sign or a square were wrong.
func TestMatchesThePaperWorkedExample(t *testing.T) {
	p := Player{Rating: 1500, Deviation: 200, Volatility: 0.06}
	got := Update(p, []Result{
		{OpponentRating: 1400, OpponentDeviation: 30, Score: 1},
		{OpponentRating: 1550, OpponentDeviation: 100, Score: 0},
		{OpponentRating: 1700, OpponentDeviation: 300, Score: 0},
	})

	for _, c := range []struct {
		name      string
		got, want float64
		tol       float64
	}{
		{"rating", got.Rating, 1464.06, 0.01},
		{"deviation", got.Deviation, 151.52, 0.01},
		{"volatility", got.Volatility, 0.05999, 0.00001},
	} {
		if math.Abs(c.got-c.want) > c.tol {
			t.Errorf("%s = %.5f, paper says %.5f", c.name, c.got, c.want)
		}
	}
}

func TestUncertainRatingsMoveFurther(t *testing.T) {
	// The whole reason for preferring this over Elo: an identical result
	// should move a new player more than an established one.
	opponent := Result{OpponentRating: 1500, OpponentDeviation: 50, Score: 1}

	newcomer := Update(Player{Rating: 1500, Deviation: 350, Volatility: 0.06}, []Result{opponent})
	regular := Update(Player{Rating: 1500, Deviation: 50, Volatility: 0.06}, []Result{opponent})

	if newcomer.Rating-1500 <= regular.Rating-1500 {
		t.Errorf("newcomer gained %.1f, regular gained %.1f; the uncertain rating should move more",
			newcomer.Rating-1500, regular.Rating-1500)
	}
}

func TestPlayingReducesUncertainty(t *testing.T) {
	p := Player{Rating: DefaultRating, Deviation: DefaultDeviation, Volatility: DefaultVolatility}
	after := Update(p, []Result{{OpponentRating: 1500, OpponentDeviation: 50, Score: 1}})
	if after.Deviation >= p.Deviation {
		t.Errorf("deviation went from %.1f to %.1f; a played game should reduce it",
			p.Deviation, after.Deviation)
	}
}

func TestIdlenessWidensDeviationButCapsIt(t *testing.T) {
	p := Player{Rating: 1700, Deviation: 60, Volatility: 0.06}
	after := Update(p, nil)
	if after.Rating != p.Rating {
		t.Errorf("rating changed with no games played: %.1f", after.Rating)
	}
	if after.Deviation <= p.Deviation {
		t.Errorf("deviation should widen while idle, went %.1f to %.1f", p.Deviation, after.Deviation)
	}

	// Left alone forever it must not exceed the default, or an old account
	// would end up less trusted than a brand new one.
	q := Player{Rating: 1700, Deviation: 349, Volatility: 0.06}
	for i := 0; i < 500; i++ {
		q = Update(q, nil)
	}
	if q.Deviation > DefaultDeviation+0.001 {
		t.Errorf("deviation drifted past the default to %.2f", q.Deviation)
	}
}

func TestBeatingAStrongerOpponentGainsMore(t *testing.T) {
	p := Player{Rating: 1500, Deviation: 100, Volatility: 0.06}
	vsWeak := Update(p, []Result{{OpponentRating: 1200, OpponentDeviation: 50, Score: 1}})
	vsStrong := Update(p, []Result{{OpponentRating: 1800, OpponentDeviation: 50, Score: 1}})

	if vsStrong.Rating <= vsWeak.Rating {
		t.Errorf("beating 1800 gave %.1f, beating 1200 gave %.1f", vsStrong.Rating, vsWeak.Rating)
	}
}
