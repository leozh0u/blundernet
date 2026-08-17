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
	// Users go too: ranked mode signs one up, and a leftover row from the last
	// run makes the second run fail on a taken username.
	if _, err := archive.Pool().Exec(ctx, "TRUNCATE puzzles, users CASCADE"); err != nil {
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

// signUp returns the cookies for a fresh account, which is what ranked mode
// requires and guests do not get.
func signUp(t *testing.T, s *Server, name string) []*http.Cookie {
	t.Helper()
	rec := do(t, s, "POST", "/api/auth/signup",
		`{"username":"`+name+`","password":"correct horse battery"}`)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}
	return rec.Result().Cookies()
}

func asUser(s *Server, cookies []*http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	req := newRequest(method, path, body)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return serve(s, req)
}

func TestRankedNeedsAnAccount(t *testing.T) {
	s := newPuzzleServer(t)
	rec := do(t, s, "GET", "/api/puzzles/ranked", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d %s", rec.Code, rec.Body)
	}
	// A guest is minted by playing a learning puzzle, and still cannot play
	// ranked: an account you can mint for free is not an account.
	attempt := do(t, s, "POST", "/api/puzzles/fork01/attempt", `{"solved":true}`)
	rec = asUser(s, attempt.Result().Cookies(), "GET", "/api/puzzles/ranked", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("guest: %d %s", rec.Code, rec.Body)
	}
}

func TestRankedServesOnePuzzleAndKeepsIt(t *testing.T) {
	s := newPuzzleServer(t)
	cookies := signUp(t, s, "ranked_keeps")

	rec := asUser(s, cookies, "GET", "/api/puzzles/ranked", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ranked: %d %s", rec.Code, rec.Body)
	}
	var first rankedView
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.SetupMove == "" {
		t.Fatalf("got %+v", first)
	}
	// The solution must not be in the payload at all. This is the whole
	// difference between the two modes.
	if bytes := rec.Body.String(); strings.Contains(bytes, "solution") {
		t.Errorf("ranked payload carries a solution: %s", bytes)
	}

	// Asking again returns the same puzzle, so an unwanted one cannot be
	// reloaded away.
	rec = asUser(s, cookies, "GET", "/api/puzzles/ranked", "")
	var again rankedView
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Errorf("reloading changed the puzzle: %s then %s", first.ID, again.ID)
	}
}

func TestRankedWrongMoveEndsTheAttemptAndMovesTheRating(t *testing.T) {
	s := newPuzzleServer(t)
	cookies := signUp(t, s, "ranked_wrong")
	asUser(s, cookies, "GET", "/api/puzzles/ranked", "")

	rec := asUser(s, cookies, "POST", "/api/puzzles/ranked/move", `{"uci":"a2a3","ms":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	var res rankedMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Correct || !res.Done {
		t.Errorf("a wrong move should end the attempt: %+v", res)
	}
	if len(res.Solution) == 0 {
		t.Error("the solution should be revealed once the attempt is over")
	}
	if res.Rating == nil || res.Rating.Change >= 0 {
		t.Errorf("a failed puzzle should cost rating, got %+v", res.Rating)
	}
	// The attempt is over, so a second move has nothing to grade.
	rec = asUser(s, cookies, "POST", "/api/puzzles/ranked/move", `{"uci":"a2a3"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("second move: %d %s", rec.Code, rec.Body)
	}
}

func TestRankedSolvingRaisesTheRating(t *testing.T) {
	s := newPuzzleServer(t)
	cookies := signUp(t, s, "ranked_right")
	asUser(s, cookies, "GET", "/api/puzzles/ranked", "")

	// Every fixture puzzle is the same line: the solver plays e6e7, the
	// opponent answers b2b1, and b3c1 finishes it.
	rec := asUser(s, cookies, "POST", "/api/puzzles/ranked/move", `{"uci":"e6e7"}`)
	var step rankedMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &step); err != nil {
		t.Fatal(err)
	}
	if !step.Correct || step.Done || step.Reply != "b2b1" {
		t.Fatalf("first move: %+v", step)
	}

	rec = asUser(s, cookies, "POST", "/api/puzzles/ranked/move", `{"uci":"b3c1","ms":9000}`)
	var last rankedMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
		t.Fatal(err)
	}
	if !last.Correct || !last.Done {
		t.Fatalf("last move: %+v", last)
	}
	if last.Rating == nil || last.Rating.Change <= 0 {
		t.Errorf("solving should gain rating, got %+v", last.Rating)
	}
	if last.Rating.Solved != 1 {
		t.Errorf("solved count = %d, want 1", last.Rating.Solved)
	}

	// The next request draws a different puzzle, because the solved one is
	// now in the seen set.
	rec = asUser(s, cookies, "GET", "/api/puzzles/ranked", "")
	var next rankedView
	if err := json.Unmarshal(rec.Body.Bytes(), &next); err != nil {
		t.Fatal(err)
	}
	if next.ID == "" {
		t.Fatal("no puzzle after finishing one")
	}
}
