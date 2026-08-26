package library

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DiscoverCard is one book as the discovery screen shows it: everything a
// reader needs to decide, in one payload, because the point of the screen is
// not having to click through to find out.
type DiscoverCard struct {
	ID          int64    `json:"id"`
	UUID        string   `json:"uuid"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Series      *string  `json:"series,omitempty"`
	SeriesIndex *float64 `json:"series_index,omitempty"`
	Description string   `json:"description,omitempty"`
	Publisher   *string  `json:"publisher,omitempty"`
	PubYear     *int     `json:"pub_year,omitempty"`
	Rating      *int16   `json:"rating,omitempty"`
	Tags        []string `json:"tags"`
	Languages   []string `json:"languages"`
	HasCover    bool     `json:"has_cover"`
	Formats     []string `json:"formats"`
}

// DiscoveryShelf returns the id of a user's keep-shelf, creating it if the
// account was made after the feature shipped.
func (s *Store) DiscoveryShelf(ctx context.Context, userID int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM shelves WHERE user_id=$1 AND is_discovery`, userID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO shelves (user_id, name, is_discovery, position)
		VALUES ($1, 'Discoveries', true, -1)
		ON CONFLICT (user_id) WHERE is_discovery DO UPDATE SET name = shelves.name
		RETURNING id`, userID).Scan(&id)
	return id, err
}

// DiscoverDeck returns the next few candidates.
//
// A deck rather than a single card: a swipe has to feel instant, so the next
// cover must already be decoded by the time the current one leaves the screen.
// One request per swipe would put a network round trip in the middle of the
// animation.
//
// Only books with a cover are offered. A card is mostly cover, and a blank one
// gives a reader nothing to react to -- which is the whole interaction.
func (s *Store) DiscoverDeck(ctx context.Context, userID int64, limit int, adult AdultVisibility) ([]DiscoverCard, error) {
	if limit <= 0 || limit > 40 {
		limit = 10
	}
	adultClause := "NOT b.adult"
	if adult == AdultInclude {
		adultClause = "true"
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		WITH shelf AS (SELECT id FROM shelves WHERE user_id = $1 AND is_discovery)
		SELECT b.id, b.uuid::text, b.title, b.author_names, b.series_name, b.series_index,
		       COALESCE(b.description, ''), p.name, EXTRACT(YEAR FROM b.pubdate)::int,
		       b.rating, b.tag_names, b.languages, b.has_cover,
		       COALESCE(ARRAY(SELECT f.format FROM book_files f
		                       WHERE f.book_id = b.id ORDER BY f.format), '{}')
		FROM books b
		LEFT JOIN publishers p ON p.id = b.publisher_id
		WHERE %s
		  AND b.has_cover
		  AND NOT EXISTS (SELECT 1 FROM discovery_passes d
		                   WHERE d.user_id = $1 AND d.book_id = b.id)
		  AND NOT EXISTS (SELECT 1 FROM shelf_books sb JOIN shelf ON sb.shelf_id = shelf.id
		                   WHERE sb.book_id = b.id)
		ORDER BY random()
		LIMIT $2`, adultClause), userID, limit)
	if err != nil {
		return nil, fmt.Errorf("discover deck: %w", err)
	}
	defer rows.Close()

	out := make([]DiscoverCard, 0, limit)
	for rows.Next() {
		var c DiscoverCard
		if err := rows.Scan(&c.ID, &c.UUID, &c.Title, &c.Authors, &c.Series, &c.SeriesIndex,
			&c.Description, &c.Publisher, &c.PubYear, &c.Rating, &c.Tags, &c.Languages,
			&c.HasCover, &c.Formats); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DiscoverKeep puts a book on the reader's shelf, and forgets any earlier pass
// so a change of mind sticks.
func (s *Store) DiscoverKeep(ctx context.Context, userID, bookID int64) error {
	shelf, err := s.DiscoveryShelf(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO shelf_books (shelf_id, book_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, shelf, bookID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM discovery_passes WHERE user_id=$1 AND book_id=$2`, userID, bookID); err != nil {
		return err
	}
	// The shelf's own timestamp drives Kobo's collection sync.
	if _, err := tx.Exec(ctx,
		`UPDATE shelves SET updated_at = now() WHERE id = $1`, shelf); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DiscoverPass records that a reader has seen this one and moved on.
func (s *Store) DiscoverPass(ctx context.Context, userID, bookID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO discovery_passes (user_id, book_id) VALUES ($1, $2)
		ON CONFLICT (user_id, book_id) DO UPDATE SET passed_at = now()`, userID, bookID)
	return err
}

// DiscoverUndo takes back the last decision, whichever way it went.
func (s *Store) DiscoverUndo(ctx context.Context, userID, bookID int64) error {
	shelf, err := s.DiscoveryShelf(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM discovery_passes WHERE user_id=$1 AND book_id=$2`, userID, bookID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM shelf_books WHERE shelf_id=$1 AND book_id=$2`, shelf, bookID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DiscoverStats is the running tally the screen shows.
type DiscoverStats struct {
	Kept      int64  `json:"kept"`
	Passed    int64  `json:"passed"`
	Remaining int64  `json:"remaining"`
	ShelfID   int64  `json:"shelf_id"`
	ShelfName string `json:"shelf_name"`
}

// DiscoverStatsFor counts what is left to look at.
func (s *Store) DiscoverStatsFor(ctx context.Context, userID int64, adult AdultVisibility) (*DiscoverStats, error) {
	shelf, err := s.DiscoveryShelf(ctx, userID)
	if err != nil {
		return nil, err
	}
	adultClause := "NOT b.adult"
	if adult == AdultInclude {
		adultClause = "true"
	}
	var st DiscoverStats
	st.ShelfID = shelf
	err = s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT (SELECT count(*) FROM shelf_books WHERE shelf_id = $2),
		       (SELECT count(*) FROM discovery_passes WHERE user_id = $1),
		       (SELECT count(*) FROM books b WHERE %s AND b.has_cover
		          AND NOT EXISTS (SELECT 1 FROM discovery_passes d
		                           WHERE d.user_id = $1 AND d.book_id = b.id)
		          AND NOT EXISTS (SELECT 1 FROM shelf_books sb
		                           WHERE sb.shelf_id = $2 AND sb.book_id = b.id)),
		       (SELECT name FROM shelves WHERE id = $2)`, adultClause),
		userID, shelf).Scan(&st.Kept, &st.Passed, &st.Remaining, &st.ShelfName)
	return &st, err
}
