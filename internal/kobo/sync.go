package kobo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SyncItemLimit caps one sync response, matching calibre-web.
//
// The device is told to come back for more via the x-kobo-sync header, so a
// large shelf arrives over several quick requests instead of one slow one. That
// matters: the firmware abandons a request after roughly 30 seconds.
const SyncItemLimit = 100

// syncBook is one book as the sync engine needs it.
type syncBook struct {
	ID          int64
	UUID        string
	Title       string
	Authors     []string
	Series      *string
	SeriesIndex *float64
	Description *string
	Publisher   *string
	PubDate     *time.Time
	Language    string
	Tags        []string
	Path        string
	Changed     time.Time
	Created     time.Time
	IsNew       bool
}

// Engine answers sync requests.
type Engine struct {
	pool *pgxpool.Pool
}

// NewEngine builds a sync engine.
func NewEngine(pool *pgxpool.Pool) *Engine { return &Engine{pool: pool} }

// changedBooks returns books on the user's Kobo-synced shelves that have moved
// since the token's watermark.
//
// This is the query that matters. calibre-web scans the whole library here even
// when the synced shelf holds a few dozen books -- measured at 13 seconds on a
// 13,000-book library, which is most of the device's timeout budget. Starting
// from shelf_books instead means the row count is the size of the shelf, not
// the size of the library.
func (e *Engine) changedBooks(ctx context.Context, userID int64, since time.Time, limit int) ([]syncBook, bool, error) {
	rows, err := e.pool.Query(ctx, `
		WITH my_shelves AS (
			-- Shelves I own and have marked for sync, plus public shelves
			-- someone else owns that I have subscribed to.
			SELECT s.id FROM shelves s
			WHERE s.user_id = $1 AND s.kobo_sync
			UNION
			SELECT s.id FROM shelves s
			JOIN shelf_kobo_subscriptions sub ON sub.shelf_id = s.id
			WHERE sub.user_id = $1 AND s.is_public
		), synced AS (
			SELECT sb.book_id, min(sb.added_at) AS added_at
			FROM shelf_books sb
			JOIN my_shelves ms ON ms.id = sb.shelf_id
			GROUP BY sb.book_id
		)
		SELECT b.id, b.uuid::text, b.title, b.author_names, b.series_name, b.series_index,
		       b.description, p.name, b.pubdate,
		       COALESCE(b.languages[1], 'en'), b.tag_names, b.path,
		       GREATEST(b.updated_at, sy.added_at) AS changed,
		       sy.added_at,
		       (ks.book_id IS NULL) AS is_new
		FROM synced sy
		JOIN books b ON b.id = sy.book_id
		LEFT JOIN publishers p       ON p.id = b.publisher_id
		LEFT JOIN kobo_synced_books ks ON ks.book_id = b.id AND ks.user_id = $1
		LEFT JOIN kobo_archived ka     ON ka.book_id = b.id AND ka.user_id = $1
		WHERE ka.book_id IS NULL
		  AND GREATEST(b.updated_at, sy.added_at) > $2
		ORDER BY GREATEST(b.updated_at, sy.added_at), b.id
		LIMIT $3`, userID, since, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []syncBook
	for rows.Next() {
		var b syncBook
		if err := rows.Scan(&b.ID, &b.UUID, &b.Title, &b.Authors, &b.Series, &b.SeriesIndex,
			&b.Description, &b.Publisher, &b.PubDate, &b.Language, &b.Tags, &b.Path,
			&b.Changed, &b.Created, &b.IsNew); err != nil {
			return nil, false, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// removedBooks lists books the device holds that have left every synced shelf.
//
// Without the tombstone table these would be invisible: the row that would tell
// us has been deleted, so the only signal is that we once synced a book that no
// longer appears in the shelf query.
func (e *Engine) removedBooks(ctx context.Context, userID int64, since time.Time, limit int) ([]string, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT b.uuid::text
		FROM kobo_synced_books ks
		JOIN books b ON b.id = ks.book_id
		WHERE ks.user_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM shelf_books sb
			JOIN shelves s ON s.id = sb.shelf_id
			LEFT JOIN shelf_kobo_subscriptions sub
			       ON sub.shelf_id = s.id AND sub.user_id = $1
			WHERE sb.book_id = ks.book_id
			  AND ((s.user_id = $1 AND s.kobo_sync)
			    OR (s.is_public AND sub.user_id IS NOT NULL)))
		ORDER BY ks.last_synced_at
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// markSynced records that the device has been told about these books.
func (e *Engine) markSynced(ctx context.Context, userID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := e.pool.Exec(ctx, `
		INSERT INTO kobo_synced_books (user_id, book_id, first_synced_at, last_synced_at)
		SELECT $1, unnest($2::bigint[]), now(), now()
		ON CONFLICT (user_id, book_id) DO UPDATE SET last_synced_at = now()`, userID, ids)
	return err
}

// forgetSynced drops books the device has been told to remove.
func (e *Engine) forgetSynced(ctx context.Context, userID int64, uuids []string) error {
	if len(uuids) == 0 {
		return nil
	}
	_, err := e.pool.Exec(ctx, `
		DELETE FROM kobo_synced_books ks
		USING books b
		WHERE ks.book_id = b.id AND ks.user_id = $1 AND b.uuid::text = ANY($2)`, userID, uuids)
	return err
}

// shelfRow is a Kobo-synced shelf.
type shelfRow struct {
	ID       int64
	UUID     string
	Name     string
	Created  time.Time
	Modified time.Time
	IsNew    bool
	Items    []string // book uuids
}

// changedShelves returns synced shelves that moved since the watermark.
func (e *Engine) changedShelves(ctx context.Context, userID int64, since time.Time) ([]shelfRow, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT s.id, s.uuid::text, s.name, s.created_at, s.updated_at,
		       (s.created_at > $2) AS is_new
		FROM shelves s
		LEFT JOIN shelf_kobo_subscriptions sub
		       ON sub.shelf_id = s.id AND sub.user_id = $1
		WHERE ((s.user_id = $1 AND s.kobo_sync)
		    OR (s.is_public AND sub.user_id IS NOT NULL))
		  AND s.updated_at > $2
		ORDER BY s.updated_at, s.id`, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []shelfRow
	for rows.Next() {
		var s shelfRow
		if err := rows.Scan(&s.ID, &s.UUID, &s.Name, &s.Created, &s.Modified, &s.IsNew); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		irows, err := e.pool.Query(ctx, `
			SELECT b.uuid::text FROM shelf_books sb
			JOIN books b ON b.id = sb.book_id
			WHERE sb.shelf_id = $1
			ORDER BY sb.position, sb.book_id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for irows.Next() {
			var u string
			if err := irows.Scan(&u); err != nil {
				irows.Close()
				return nil, err
			}
			out[i].Items = append(out[i].Items, u)
		}
		irows.Close()
		if err := irows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// deletedShelves returns tombstones for shelves removed since the watermark.
func (e *Engine) deletedShelves(ctx context.Context, userID int64, since time.Time) ([]string, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT uuid::text FROM deleted_shelves
		WHERE user_id = $1 AND deleted_at > $2
		ORDER BY deleted_at`, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// readingStateRow is stored progress for one book.
type readingStateRow struct {
	BookUUID     string
	Status       string
	Progress     *int
	SrcProgress  *int
	LocValue     *string
	LocType      *string
	LocSource    *string
	Spent        *int
	Remaining    *int
	TimesStarted int
	LastStarted  *time.Time
	LastFinished *time.Time
	Priority     time.Time
	Modified     time.Time
}

// changedReadingStates returns progress updated since the watermark, scoped to
// synced shelves so an unrelated book never appears.
func (e *Engine) changedReadingStates(ctx context.Context, userID int64, since time.Time, limit int) ([]readingStateRow, bool, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT b.uuid::text, rs.status, rs.progress_percent,
		       rs.content_source_progress_percent, rs.location_value, rs.location_type,
		       rs.location_source, rs.spent_reading_minutes, rs.remaining_time_minutes,
		       rs.times_started_reading, rs.last_time_started_reading,
		       rs.last_time_finished, rs.priority_timestamp, rs.last_modified
		FROM reading_state rs
		JOIN books b ON b.id = rs.book_id
		WHERE rs.user_id = $1 AND rs.last_modified > $2
		  AND EXISTS (
			SELECT 1 FROM shelf_books sb
			JOIN shelves s ON s.id = sb.shelf_id
			LEFT JOIN shelf_kobo_subscriptions sub
			       ON sub.shelf_id = s.id AND sub.user_id = $1
			WHERE sb.book_id = rs.book_id
			  AND ((s.user_id = $1 AND s.kobo_sync)
			    OR (s.is_public AND sub.user_id IS NOT NULL)))
		ORDER BY rs.last_modified, rs.book_id
		LIMIT $3`, userID, since, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []readingStateRow
	for rows.Next() {
		var s readingStateRow
		if err := rows.Scan(&s.BookUUID, &s.Status, &s.Progress, &s.SrcProgress,
			&s.LocValue, &s.LocType, &s.LocSource, &s.Spent, &s.Remaining,
			&s.TimesStarted, &s.LastStarted, &s.LastFinished, &s.Priority,
			&s.Modified); err != nil {
			return nil, false, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}
