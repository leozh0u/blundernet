package store

import "context"

// Favourite saves a puzzle for later. Saving one that is already saved is not
// an error: the button is a toggle and a double click should not 500.
func (p *Puzzles) Favourite(ctx context.Context, userID, puzzleID string) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO puzzle_favourites (user_id, puzzle_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, puzzleID)
	return err
}

func (p *Puzzles) Unfavourite(ctx context.Context, userID, puzzleID string) error {
	_, err := p.pool.Exec(ctx,
		"DELETE FROM puzzle_favourites WHERE user_id = $1 AND puzzle_id = $2",
		userID, puzzleID)
	return err
}

// IsFavourite answers for one puzzle, which is what the star on the panel
// needs when a puzzle is served.
func (p *Puzzles) IsFavourite(ctx context.Context, userID, puzzleID string) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM puzzle_favourites WHERE user_id = $1 AND puzzle_id = $2
		)`, userID, puzzleID).Scan(&exists)
	return exists, err
}

// FavouriteIDs returns saved puzzles, newest first.
func (p *Puzzles) FavouriteIDs(ctx context.Context, userID string, limit int) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT puzzle_id FROM puzzle_favourites
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FavouriteSet is the whole set for one user, for marking a batch of search
// results without a query per puzzle.
func (p *Puzzles) FavouriteSet(ctx context.Context, userID string) (map[string]bool, error) {
	if userID == "" {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx,
		"SELECT puzzle_id FROM puzzle_favourites WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
