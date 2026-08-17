package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leozh0u/blundernet/internal/puzzle"
)

type Puzzles struct {
	pool *pgxpool.Pool
}

func NewPuzzles(pool *pgxpool.Pool) *Puzzles { return &Puzzles{pool: pool} }

// PuzzleSource is a stream of puzzles to load. It is an interface rather than
// a slice because the Lichess dump is six million rows: the loader streams the
// file straight into the COPY protocol instead of holding it in memory.
type PuzzleSource interface {
	Next() bool
	Puzzle() puzzle.Puzzle
	Err() error
}

// stagingDDL matches the columns the loader writes, not the whole table. The
// counters this site keeps about its own users are deliberately absent.
//
// TEMP rather than a plain table: it is private to one connection, so two
// loads running at once cannot drop each other's staging table halfway
// through, and it is not written to the write-ahead log either, which is the
// other reason a staging table wants to be cheap.
const stagingDDL = `
	CREATE TEMP TABLE puzzles_staging (
	    id             TEXT NOT NULL,
	    fen            TEXT NOT NULL,
	    moves          TEXT NOT NULL,
	    rating         DOUBLE PRECISION NOT NULL,
	    rating_deviation DOUBLE PRECISION NOT NULL,
	    solution_plies INT NOT NULL,
	    phase          TEXT NOT NULL,
	    themes         TEXT[] NOT NULL,
	    popularity     INT NOT NULL,
	    nb_plays       INT NOT NULL,
	    game_url       TEXT NOT NULL,
	    opening_tags   TEXT[] NOT NULL
	)`

var stagingColumns = []string{
	"id", "fen", "moves", "rating", "rating_deviation", "solution_plies",
	"phase", "themes", "popularity", "nb_plays", "game_url", "opening_tags",
}

// Load streams puzzles into a staging table and then merges them into the
// real one. Two steps rather than one because this is re-run monthly against
// a fresh dump: a straight COPY into puzzles would collide on every row that
// already exists, and a per-row upsert of six million rows takes hours.
//
// Everything happens on one connection held for the whole load, because the
// staging table is temporary and a temporary table only exists for the session
// that made it.
//
// Returns how many rows were copied and how many ended up in puzzles.
func (p *Puzzles) Load(ctx context.Context, src PuzzleSource) (copied, merged int64, err error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "DROP TABLE IF EXISTS puzzles_staging"); err != nil {
		return 0, 0, fmt.Errorf("drop staging: %w", err)
	}
	if _, err := conn.Exec(ctx, stagingDDL); err != nil {
		return 0, 0, fmt.Errorf("create staging: %w", err)
	}
	defer func() {
		// Pooled connections are reused, so the table is dropped rather than
		// left for the session to clean up whenever it eventually ends.
		if _, dropErr := conn.Exec(context.WithoutCancel(ctx),
			"DROP TABLE IF EXISTS puzzles_staging"); dropErr != nil && err == nil {
			err = fmt.Errorf("drop staging: %w", dropErr)
		}
	}()

	rows := &copySource{src: src}
	copied, err = conn.CopyFrom(ctx,
		pgx.Identifier{"puzzles_staging"}, stagingColumns, rows)
	if err != nil {
		return copied, 0, fmt.Errorf("copy: %w", err)
	}

	// A dump can carry the same id twice only if Lichess made a mistake, but
	// ON CONFLICT cannot handle two conflicting rows in one statement, so the
	// merge deduplicates first and fails loudly rather than half loading.
	tag, err := conn.Exec(ctx, `
		INSERT INTO puzzles (id, fen, moves, rating, rating_deviation,
		                     solution_plies, phase, themes, popularity,
		                     nb_plays, game_url, opening_tags, source)
		SELECT DISTINCT ON (id) id, fen, moves, rating, rating_deviation,
		       solution_plies, phase, themes, popularity,
		       nb_plays, game_url, opening_tags, 'lichess'
		FROM puzzles_staging
		ORDER BY id
		ON CONFLICT (id) DO UPDATE SET
		    fen              = EXCLUDED.fen,
		    moves            = EXCLUDED.moves,
		    rating           = EXCLUDED.rating,
		    rating_deviation = EXCLUDED.rating_deviation,
		    solution_plies   = EXCLUDED.solution_plies,
		    phase            = EXCLUDED.phase,
		    themes           = EXCLUDED.themes,
		    popularity       = EXCLUDED.popularity,
		    nb_plays         = EXCLUDED.nb_plays,
		    game_url         = EXCLUDED.game_url,
		    opening_tags     = EXCLUDED.opening_tags`)
	if err != nil {
		return copied, 0, fmt.Errorf("merge: %w", err)
	}
	return copied, tag.RowsAffected(), nil
}

// Count reports how many puzzles are loaded. An estimate would be cheaper but
// this is only called by the loader and by tests, where an exact number is
// what is wanted.
func (p *Puzzles) Count(ctx context.Context) (int64, error) {
	var n int64
	err := p.pool.QueryRow(ctx, "SELECT count(*) FROM puzzles").Scan(&n)
	return n, err
}

// Analyze refreshes the planner's statistics. Worth doing explicitly after a
// bulk load: autovacuum gets there eventually, and until it does the planner
// is choosing between index and sequential scans from statistics that predate
// six million rows.
func (p *Puzzles) Analyze(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, "ANALYZE puzzles")
	return err
}

// RefreshCells rebuilds the cell summary the sampler draws from. Called by the
// loader, because the counts only change when an import does.
func (p *Puzzles) RefreshCells(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err := tx.Exec(ctx, "DELETE FROM puzzle_cells"); err != nil {
		return err
	}
	// The '' rows count whole cells, the rest count one theme inside a cell.
	if _, err := tx.Exec(ctx, `
		INSERT INTO puzzle_cells (theme, rating_band, phase, solution_plies, n)
		SELECT '', rating_band, phase, solution_plies, count(*)
		FROM puzzles
		GROUP BY 2, 3, 4`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO puzzle_cells (theme, rating_band, phase, solution_plies, n)
		SELECT theme, rating_band, phase, solution_plies, count(*)
		FROM puzzles, unnest(themes) AS theme
		GROUP BY 1, 2, 3, 4`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// copySource adapts a PuzzleSource to the pgx COPY interface. Errors from the
// underlying source surface through Err, which is where pgx looks.
type copySource struct {
	src PuzzleSource
}

func (c *copySource) Next() bool { return c.src.Next() }

func (c *copySource) Values() ([]any, error) {
	p := c.src.Puzzle()
	themes := p.Themes
	if themes == nil {
		themes = []string{}
	}
	openings := p.OpeningTags
	if openings == nil {
		openings = []string{}
	}
	return []any{
		p.ID, p.FEN, joinMoves(p.Moves), p.Rating, p.RatingDev,
		p.SolutionPlies, p.Phase, themes, p.Popularity, p.NbPlays,
		p.GameURL, openings,
	}, nil
}

func (c *copySource) Err() error { return c.src.Err() }
