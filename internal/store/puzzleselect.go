package store

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/leozh0u/blundernet/internal/puzzle"
)

// Filter is a puzzle search. Every field is optional; the zero value matches
// everything.
type Filter struct {
	MinRating, MaxRating int
	Phases               []string // any of, empty for all
	MinPlies, MaxPlies   int      // solution length in plies, 0 for unbounded
	Themes               []string // all of
	Openings             []string // any of, since a puzzle carries one line
	MinPopularity        int      // Lichess upvote score, -100 accepts everything
}

// openingPrefix keeps openings and themes apart inside the one summary table.
const openingPrefix = "op:"

// tags is every filter the sampler weights cells by. The rarest of them
// decides which cells can answer at all.
func (f Filter) tags() []string {
	out := make([]string, 0, len(f.Themes)+len(f.Openings))
	out = append(out, f.Themes...)
	for _, o := range f.Openings {
		out = append(out, openingPrefix+o)
	}
	return out
}

// cell is one square of the filter grid: an exact rating band, phase and
// solution length. Selection happens inside a cell because that makes every
// indexed column an equality and leaves sample_key free to be the range, which
// is the difference between a 1ms scan and a 1.4s sort.
type cell struct {
	band  int16
	phase string
	plies int
	n     int64
}

// candidatesPerScan is how many rows a single cell scan asks for. A little
// over the batch the drill asks for, because rows are dropped afterwards for
// being already seen and a second round trip costs more than a few spare rows.
//
// It was 32, and on this box that was the difference between working and not.
// A themed scan walks the shuffle rechecking the theme against the heap, and
// the common themes sit about one row in ten to one in thirty-six, so the rows
// walked scale with this number and every one of them is a random page. A
// skewer scan measured 885 pages at 32 and 272 at 12.
const candidatesPerScan = 12

// maxScans bounds the work one Select can do. A filter that matches almost
// nothing must fail fast rather than walk every cell it was given.
const maxScans = 12

// Select returns up to n puzzles matching the filter, skipping any id that
// seen reports. seen may be nil.
//
// The shape of this is: draw a cell in proportion to how many puzzles it
// holds, then range scan that cell from a random cursor in the shuffled
// order, wrapping to the start if the cursor lands near the end. Drawing cells
// in proportion is what keeps the result uniform over the whole matching set
// rather than uniform over cells, which would make thirteen move puzzles as
// common as three move ones.
func (p *Puzzles) Select(ctx context.Context, f Filter, n int, seen func(id string) bool) ([]puzzle.Puzzle, error) {
	if n <= 0 {
		return nil, nil
	}
	cells, total, err := p.cells(ctx, f)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}

	out := make([]puzzle.Puzzle, 0, n)
	picked := make(map[string]bool, n)
	pool, left := slices.Clone(cells), total
	for scans := 0; len(out) < n && scans < maxScans; scans++ {
		if left == 0 {
			// Every cell has been visited once and the caller still wants
			// more. Put them all back: a second visit to a big cell lands on
			// a different cursor and returns different puzzles.
			pool, left = slices.Clone(cells), total
		}
		c := drawCell(pool, left)
		left -= c.n

		// A cell holding no more than one scan's worth is read whole. The
		// cursor exists to avoid returning the same rows every time, and
		// there is nothing to avoid when the answer is all of them.
		var (
			rows []puzzle.Puzzle
			err  error
		)
		if c.n <= candidatesPerScan {
			rows, err = p.scanWholeCell(ctx, c, f, candidatesPerScan)
			if err != nil {
				return nil, err
			}
		} else {
			cursor := int32(rand.Uint32())
			rows, err = p.scanCell(ctx, c, f, cursor, candidatesPerScan)
			if err != nil {
				return nil, err
			}
			// A cursor landing near the end of the shuffle finds few rows
			// above it, so the scan wraps to the start rather than returning
			// short and making the tail of every cell unreachable.
			if len(rows) < candidatesPerScan {
				more, err := p.scanCellWrapped(ctx, c, f, cursor, candidatesPerScan-len(rows))
				if err != nil {
					return nil, err
				}
				rows = append(rows, more...)
			}
		}
		for _, r := range rows {
			if len(out) >= n {
				break
			}
			if picked[r.ID] || (seen != nil && seen(r.ID)) {
				continue
			}
			picked[r.ID] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// One returns a single puzzle, which is what ranked mode asks for.
func (p *Puzzles) One(ctx context.Context, f Filter, seen func(id string) bool) (puzzle.Puzzle, bool, error) {
	rows, err := p.Select(ctx, f, 1, seen)
	if err != nil || len(rows) == 0 {
		return puzzle.Puzzle{}, false, err
	}
	return rows[0], true, nil
}

// cells reads the cells a filter covers, with the count that weights each one.
//
// When themes are asked for, the weights come from the rarest of them, since
// that is the one that decides whether a cell can answer at all. The others
// stay rechecks, so the weighting is approximate for a multi theme filter and
// exact for the single theme case, which is what the drill actually sends.
//
// Popularity is not in the summary either. It is a near constant filter that
// removes about one puzzle in fifty, so leaving it out of the weights costs
// nothing worth a wider table.
func (p *Puzzles) cells(ctx context.Context, f Filter) ([]cell, int64, error) {
	rows, err := p.pool.Query(ctx, `
		WITH rarest AS (
		    SELECT coalesce((
		        SELECT theme FROM puzzle_cells
		        WHERE theme = ANY ($6)
		        GROUP BY theme ORDER BY sum(n) LIMIT 1
		    ), '') AS theme
		)
		SELECT rating_band, phase, solution_plies, n
		FROM puzzle_cells, rarest
		WHERE puzzle_cells.theme = rarest.theme
		  AND ($1 = 0 OR rating_band >= $1 / 100)
		  AND ($2 = 0 OR rating_band <= $2 / 100)
		  AND (cardinality($3::text[]) = 0 OR phase = ANY ($3))
		  AND ($4 = 0 OR solution_plies >= $4)
		  AND ($5 = 0 OR solution_plies <= $5)`,
		f.MinRating, f.MaxRating, notNull(f.Phases), f.MinPlies, f.MaxPlies,
		notNull(f.tags()))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var (
		out   []cell
		total int64
	)
	for rows.Next() {
		var c cell
		if err := rows.Scan(&c.band, &c.phase, &c.plies, &c.n); err != nil {
			return nil, 0, err
		}
		if c.n == 0 {
			continue
		}
		total += c.n
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// Theme is one filter option with the size of the set behind it.
type Theme struct {
	Name string `json:"name"`
	N    int64  `json:"n"`
}

// Themes lists every theme in the corpus, largest first. It reads the same
// summary the sampler weights cells with, so the menu cannot offer a filter
// that returns nothing.
func (p *Puzzles) Themes(ctx context.Context) ([]Theme, error) {
	return p.tagList(ctx, "theme <> '' AND theme NOT LIKE 'op:%'", 0)
}

// Openings lists the most common openings. Capped, because there are 1,589 of
// them and a menu of 1,589 is not a filter, it is a haystack.
func (p *Puzzles) Openings(ctx context.Context, limit int) ([]Theme, error) {
	list, err := p.tagList(ctx, "theme LIKE 'op:%'", limit)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Name = strings.TrimPrefix(list[i].Name, openingPrefix)
	}
	return list, nil
}

func (p *Puzzles) tagList(ctx context.Context, where string, limit int) ([]Theme, error) {
	q := fmt.Sprintf(`
		SELECT theme, sum(n) FROM puzzle_cells
		WHERE %s
		GROUP BY theme
		ORDER BY sum(n) DESC`, where)
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Theme{}
	for rows.Next() {
		var t Theme
		if err := rows.Scan(&t.Name, &t.N); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// notNull turns a nil slice into an empty array. A nil slice reaches Postgres
// as NULL, and every comparison against NULL is NULL rather than false, so an
// unset filter would silently match nothing instead of everything.
func notNull(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// drawCell picks one cell with probability proportional to its size and takes
// it out of the pool, so a second scan looks somewhere new. Without that, a
// filter spanning three cells would keep re-drawing the largest one and never
// reach the puzzles in the other two.
func drawCell(cells []cell, total int64) cell {
	r := rand.Int64N(total)
	for i := range cells {
		if r < cells[i].n {
			c := cells[i]
			cells[i].n = 0
			return c
		}
		r -= cells[i].n
	}
	// Only reachable if total disagrees with the slice, which it does not.
	last := len(cells) - 1
	c := cells[last]
	cells[last].n = 0
	return c
}

const selectColumns = `id, fen, moves, rating, rating_deviation, popularity,
	nb_plays, themes, game_url, opening_tags, solution_plies, phase`

func (p *Puzzles) scanCell(ctx context.Context, c cell, f Filter, cursor int32, limit int) ([]puzzle.Puzzle, error) {
	return p.scan(ctx, c, f, "sample_key >= $4", "sample_key", cursor, limit)
}

func (p *Puzzles) scanCellWrapped(ctx context.Context, c cell, f Filter, cursor int32, limit int) ([]puzzle.Puzzle, error) {
	return p.scan(ctx, c, f, "sample_key < $4", "sample_key DESC", cursor, limit)
}

// scanWholeCell reads a cell from the very start of the shuffle, for cells
// small enough that a random cursor would only cost a second round trip to
// wrap around.
func (p *Puzzles) scanWholeCell(ctx context.Context, c cell, f Filter, limit int) ([]puzzle.Puzzle, error) {
	return p.scan(ctx, c, f, "sample_key >= $4", "sample_key", math.MinInt32, limit)
}

// scan runs one cell scan. The rating, theme and popularity predicates are
// rechecks on rows the index already narrowed, so they cost a comparison
// each rather than a scan.
func (p *Puzzles) scan(ctx context.Context, c cell, f Filter, cursorCond, order string, cursor int32, limit int) ([]puzzle.Puzzle, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM puzzles
		WHERE rating_band = $1 AND phase = $2 AND solution_plies = $3
		  AND %s
		  AND ($5 = 0 OR rating >= $5) AND ($6 = 0 OR rating <= $6)
		  AND (cardinality($7::text[]) = 0 OR themes @> $7)
		  AND (cardinality($8::text[]) = 0 OR opening_tags && $8)
		  AND popularity >= $9
		ORDER BY %s
		LIMIT $10`, selectColumns, cursorCond, order)

	rows, err := p.pool.Query(ctx, q,
		c.band, c.phase, c.plies, cursor,
		f.MinRating, f.MaxRating, notNull(f.Themes), notNull(f.Openings),
		f.MinPopularity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []puzzle.Puzzle
	for rows.Next() {
		p, err := scanPuzzle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ByID looks up one puzzle, which is what a shared link and "another like
// this" both start from.
func (p *Puzzles) ByID(ctx context.Context, id string) (puzzle.Puzzle, bool, error) {
	rows, err := p.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM puzzles WHERE id = $1", selectColumns), id)
	if err != nil {
		return puzzle.Puzzle{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return puzzle.Puzzle{}, false, rows.Err()
	}
	out, err := scanPuzzle(rows)
	if err != nil {
		return puzzle.Puzzle{}, false, err
	}
	return out, true, nil
}

// ByIDs loads several puzzles at once, in no particular order. Used by the
// wrong-answer drill, which decides what to fetch from the attempts table and
// then needs the puzzles behind those ids.
func (p *Puzzles) ByIDs(ctx context.Context, ids []string) ([]puzzle.Puzzle, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM puzzles WHERE id = ANY ($1)", selectColumns), ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[string]puzzle.Puzzle, len(ids))
	for rows.Next() {
		q, err := scanPuzzle(rows)
		if err != nil {
			return nil, err
		}
		byID[q.ID] = q
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Returned in the order asked for, because the caller's order is newest
	// failure first and that is what the drill list shows.
	out := make([]puzzle.Puzzle, 0, len(byID))
	for _, id := range ids {
		if q, ok := byID[id]; ok {
			out = append(out, q)
		}
	}
	return out, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPuzzle(row scanner) (puzzle.Puzzle, error) {
	var (
		p     puzzle.Puzzle
		moves string
	)
	err := row.Scan(&p.ID, &p.FEN, &moves, &p.Rating, &p.RatingDev, &p.Popularity,
		&p.NbPlays, &p.Themes, &p.GameURL, &p.OpeningTags, &p.SolutionPlies, &p.Phase)
	if err != nil {
		return puzzle.Puzzle{}, err
	}
	p.Moves = strings.Fields(moves)
	return p, nil
}
