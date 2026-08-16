package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/puzzle"
	"github.com/leozh0u/blundernet/internal/store"
)

// The puzzle routes are a thin layer over SQL, so they are tested against a
// real database. They skip without one, the same as the store tests.
func newPuzzleServer(t *testing.T) *Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	archive, err := store.NewArchive(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(archive.Close)
	if _, err := archive.Pool().Exec(ctx, "TRUNCATE puzzles CASCADE"); err != nil {
		t.Fatal(err)
	}
	puzzles := store.NewPuzzles(archive.Pool())
	if _, _, err := puzzles.Load(ctx, &testSource{rows: testPuzzles()}); err != nil {
		t.Fatal(err)
	}
	if err := puzzles.RefreshCells(ctx); err != nil {
		t.Fatal(err)
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}}
	return New(Deps{
		Games: store.NewGames(rdb), Archive: archive,
		Users: store.NewUsers(archive.Pool()), Puzzles: puzzles,
		Jobs: &captureQueue{}, Redis: rdb, Static: static,
	})
}

type testSource struct {
	rows []puzzle.Puzzle
	i    int
}

func (s *testSource) Next() bool            { s.i++; return s.i <= len(s.rows) }
func (s *testSource) Puzzle() puzzle.Puzzle { return s.rows[s.i-1] }
func (s *testSource) Err() error            { return nil }

func testPuzzles() []puzzle.Puzzle {
	var out []puzzle.Puzzle
	for i := 0; i < 40; i++ {
		p := puzzle.Puzzle{
			ID:  fmt.Sprintf("fork%02d", i),
			FEN: "r6k/pp2r2p/4Rp1Q/3p4/8/1N1P2R1/PqP2bPP/7K b - - 0 24",
			// Four moves, so the solver plays two and the setup move is the
			// opponent's blunder.
			Moves: []string{"f2g3", "e6e7", "b2b1", "b3c1"},
			// The first ten are easy forks, the rest hard pins, so a filter
			// has something to separate.
			Rating: 1450, Popularity: 90, SolutionPlies: 3,
			Phase: puzzle.PhaseMiddlegame, Themes: []string{"fork"},
		}
		if i >= 10 {
			p.Rating = 2050
			p.Themes = []string{"pin"}
			p.Phase = puzzle.PhaseEndgame
		}
		out = append(out, p)
	}
	return out
}

type searchResponse struct {
	Puzzles []puzzleView `json:"puzzles"`
}

// do covers the common case; these two exist for the one test that has to
// carry a session cookie from one request to the next.
func newRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestPuzzleSearchFilters(t *testing.T) {
	s := newPuzzleServer(t)

	rec := do(t, s, "GET", "/api/puzzles?rating_min=1400&rating_max=1600&theme=fork&limit=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body)
	}
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Puzzles) != 5 {
		t.Fatalf("got %d puzzles, want 5", len(got.Puzzles))
	}
	for _, p := range got.Puzzles {
		if p.Rating != 1450 {
			t.Errorf("puzzle %s has rating %d, outside the filter", p.ID, p.Rating)
		}
		// The solver is black here: the FEN has black to move and that move is
		// the blunder being punished.
		if p.Color != "white" || p.SetupMove != "f2g3" {
			t.Errorf("puzzle %s: color %q setup %q", p.ID, p.Color, p.SetupMove)
		}
		if p.Moves != 2 || len(p.Solution) != 3 {
			t.Errorf("puzzle %s: %d moves, %d solution plies", p.ID, p.Moves, len(p.Solution))
		}
	}
}

func TestPuzzleSearchRejectsAnUnknownPhase(t *testing.T) {
	s := newPuzzleServer(t)
	rec := do(t, s, "GET", "/api/puzzles?phase=lategame", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestPuzzleSearchByLength(t *testing.T) {
	s := newPuzzleServer(t)
	// Every fixture puzzle is two solver moves, so asking for three returns
	// nothing rather than falling back to something close.
	rec := do(t, s, "GET", "/api/puzzles?moves_min=3&moves_max=3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body)
	}
	var got searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Puzzles) != 0 {
		t.Errorf("got %d puzzles for a length nothing matches", len(got.Puzzles))
	}
}

// An attempt mints a guest, records the row, and marks the puzzle seen so the
// next search does not serve it again.
func TestPuzzleAttemptIsRecordedAndNotServedAgain(t *testing.T) {
	s := newPuzzleServer(t)

	rec := do(t, s, "POST", "/api/puzzles/fork00/attempt", `{"solved":false,"ms":4200,"hints":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempt: %d %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatal("an attempt should mint a session for a signed out visitor")
	}

	req := newRequest("GET", "/api/puzzles?theme=fork&limit=20", "")
	for _, c := range cookie {
		req.AddCookie(c)
	}
	rec2 := serve(s, req)
	var got searchResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Puzzles) == 0 {
		t.Fatal("no puzzles returned")
	}
	for _, p := range got.Puzzles {
		if p.ID == "fork00" {
			t.Error("an attempted puzzle came back in the next search")
		}
	}
}

func TestPuzzleAttemptOnAMissingPuzzle(t *testing.T) {
	s := newPuzzleServer(t)
	rec := do(t, s, "POST", "/api/puzzles/nosuch/attempt", `{"solved":true}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestPuzzleThemes(t *testing.T) {
	s := newPuzzleServer(t)
	rec := do(t, s, "GET", "/api/puzzles/themes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("themes: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Themes []store.Theme `json:"themes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, th := range got.Themes {
		counts[th.Name] = th.N
	}
	if counts["fork"] != 10 || counts["pin"] != 30 {
		t.Errorf("theme counts = %v, want fork 10 and pin 30", counts)
	}
}
