package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Karlmit/Klaras-Library/internal/jobs"
)

// jobsKindFileMove is the queue kind for relocating a book's files.
const jobsKindFileMove = jobs.KindFileMove

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// bookEdit is a partial update. Pointers distinguish "not supplied" from
// "explicitly cleared", which matters for bulk edits where most fields are
// deliberately left alone.
type bookEdit struct {
	Title       *string   `json:"title"`
	TitleSort   *string   `json:"title_sort"`
	Authors     *[]string `json:"authors"`
	Series      *string   `json:"series"`
	SeriesIndex *float64  `json:"series_index"`
	Publisher   *string   `json:"publisher"`
	PubDate     *string   `json:"pubdate"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
	Languages   *[]string `json:"languages"`
	Rating      *int16    `json:"rating"`
	NeedsReview *bool     `json:"needs_review"`
	// ISBN is the one identifier worth editing by hand. It is the key the
	// providers are searched by, so a book without one is unreachable for
	// blurbs and cover matching until somebody supplies it.
	ISBN *string `json:"isbn"`
}

// touchesPath reports whether an edit changes anything the managed tree's
// layout depends on, and therefore needs the book's files moved.
func (e *bookEdit) touchesPath() bool {
	return e.Title != nil || e.Authors != nil || e.Series != nil || e.SeriesIndex != nil
}

func (s *Server) handleUpdateBook(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	var e bookEdit
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&e); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if err := s.applyEdit(r.Context(), id, &e); err != nil {
		s.fail(w, r, err, "update book")
		return
	}
	s.scheduleMove(r.Context(), []int64{id}, &e)

	b, err := s.lib.GetBook(r.Context(), id, s.currentUser(r).ID)
	if err != nil {
		s.fail(w, r, err, "reload book")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// handleBulkUpdate applies one edit to many books.
//
// calibre-web has no bulk metadata edit at all, and with 1,115 books flagged
// for a merged author name it is the single most useful thing to have here.
func (s *Server) handleBulkUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs  []int64  `json:"ids"`
		Edit bookEdit `json:"edit"`
		// AddTags/RemoveTags are additive rather than replacing, which is
		// almost always what bulk tagging means.
		AddTags    []string `json:"add_tags"`
		RemoveTags []string `json:"remove_tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if len(in.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "no books selected")
		return
	}
	if len(in.IDs) > 5000 {
		writeErr(w, http.StatusBadRequest, "too many books in one request (max 5000)")
		return
	}

	for _, id := range in.IDs {
		if err := s.applyEdit(r.Context(), id, &in.Edit); err != nil {
			s.fail(w, r, err, "bulk update")
			return
		}
		if len(in.AddTags) > 0 || len(in.RemoveTags) > 0 {
			if err := s.adjustTags(r.Context(), id, in.AddTags, in.RemoveTags); err != nil {
				s.fail(w, r, err, "bulk tag")
				return
			}
		}
	}
	s.scheduleMove(r.Context(), in.IDs, &in.Edit)

	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "count": len(in.IDs)})
}

// applyEdit writes one book's metadata inside a transaction.
func (s *Server) applyEdit(ctx context.Context, id int64, e *bookEdit) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		UPDATE books SET
			title        = COALESCE($2, title),
			title_sort   = COALESCE($3, title_sort),
			series_index = CASE WHEN $4::numeric IS NOT NULL THEN $4 ELSE series_index END,
			description  = CASE WHEN $5::text IS NOT NULL THEN NULLIF($5,'') ELSE description END,
			languages    = COALESCE($6, languages),
			rating       = CASE WHEN $7::smallint IS NOT NULL THEN NULLIF($7,-1) ELSE rating END,
			needs_review = COALESCE($8, needs_review)
		WHERE id = $1`,
		id, e.Title, e.TitleSort, e.SeriesIndex, e.Description,
		arrOrNil(e.Languages), e.Rating, e.NeedsReview); err != nil {
		return err
	}

	// Retitling should refresh the sort key unless one was given explicitly,
	// or an edited title would keep sorting under its old name.
	if e.Title != nil && e.TitleSort == nil {
		if _, err := tx.Exec(ctx,
			`UPDATE books SET title_sort = title_sort_of(title) WHERE id=$1`, id); err != nil {
			return err
		}
	}

	if e.Authors != nil {
		if err := setAuthors(ctx, tx, id, *e.Authors); err != nil {
			return err
		}
	}
	if e.Tags != nil {
		if err := setTags(ctx, tx, id, *e.Tags); err != nil {
			return err
		}
	}
	if e.Series != nil {
		if err := setSeries(ctx, tx, id, *e.Series); err != nil {
			return err
		}
	}
	if e.Publisher != nil {
		if err := setPublisher(ctx, tx, id, *e.Publisher); err != nil {
			return err
		}
	}
	if e.ISBN != nil {
		if err := setISBN(ctx, tx, id, *e.ISBN); err != nil {
			return err
		}
	}
	if e.PubDate != nil {
		var d any
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(*e.PubDate)); err == nil {
			d = t
		}
		if _, err := tx.Exec(ctx, `UPDATE books SET pubdate=$2 WHERE id=$1`, id, d); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// setISBN replaces a book's ISBN, or clears it when given nothing.
//
// Only the isbn scheme is touched: a book may carry a Calibre uuid, a Google
// volume id and others, and replacing the whole identifier set to change one
// value would quietly discard them.
func setISBN(ctx context.Context, tx pgx.Tx, bookID int64, raw string) error {
	v := strings.Map(func(r rune) rune {
		// ISBNs are written with hyphens and spaces about as often as without,
		// and an X check digit is legal. Storing them one way means a search
		// finds them however they were typed.
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))

	if _, err := tx.Exec(ctx,
		`DELETE FROM identifiers WHERE book_id=$1 AND scheme='isbn'`, bookID); err != nil {
		return err
	}
	if v == "" {
		return nil
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO identifiers (book_id, scheme, value) VALUES ($1,'isbn',$2)
		 ON CONFLICT DO NOTHING`, bookID, v)
	return err
}

func arrOrNil(p *[]string) any {
	if p == nil {
		return nil
	}
	return *p
}

// upsertNamed resolves a name to an id, creating the row if needed.
func upsertNamed(ctx context.Context, tx pgx.Tx, table, name string) (int64, error) {
	var id int64
	// ON CONFLICT DO UPDATE rather than DO NOTHING: DO NOTHING returns no row
	// when the name already exists, which would need a second round trip.
	err := tx.QueryRow(ctx,
		"INSERT INTO "+table+" (name) VALUES ($1) ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name RETURNING id",
		name).Scan(&id)
	return id, err
}

func setAuthors(ctx context.Context, tx pgx.Tx, bookID int64, names []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM book_authors WHERE book_id=$1`, bookID); err != nil {
		return err
	}
	for i, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO authors (name, sort) VALUES ($1, author_sort_of($1))
			ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name
			RETURNING id`, name).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO book_authors (book_id, author_id, position) VALUES ($1,$2,$3)
			 ON CONFLICT DO NOTHING`, bookID, id, i); err != nil {
			return err
		}
	}
	return nil
}

func setTags(ctx context.Context, tx pgx.Tx, bookID int64, names []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM book_tags WHERE book_id=$1`, bookID); err != nil {
		return err
	}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		id, err := upsertNamed(ctx, tx, "tags", name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO book_tags (book_id, tag_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			bookID, id); err != nil {
			return err
		}
	}
	return nil
}

func setSeries(ctx context.Context, tx pgx.Tx, bookID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		_, err := tx.Exec(ctx,
			`UPDATE books SET series_id=NULL, series_index=NULL, series_name=NULL WHERE id=$1`, bookID)
		return err
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO series (name, sort) VALUES ($1,$1)
		ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name RETURNING id`, name).Scan(&id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE books SET series_id=$2 WHERE id=$1`, bookID, id)
	return err
}

func setPublisher(ctx context.Context, tx pgx.Tx, bookID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		_, err := tx.Exec(ctx, `UPDATE books SET publisher_id=NULL WHERE id=$1`, bookID)
		return err
	}
	id, err := upsertNamed(ctx, tx, "publishers", name)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE books SET publisher_id=$2 WHERE id=$1`, bookID, id)
	return err
}

// adjustTags adds and removes tags without replacing the whole set.
func (s *Server) adjustTags(ctx context.Context, bookID int64, add, remove []string) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, raw := range add {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		id, err := upsertNamed(ctx, tx, "tags", name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO book_tags (book_id, tag_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			bookID, id); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM book_tags bt USING tags t
			WHERE bt.tag_id=t.id AND bt.book_id=$1 AND t.name = ANY($2)`,
			bookID, remove); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// scheduleMove queues file relocation for books whose layout inputs changed.
//
// Queued per book, never as a library-wide sweep: renaming one author must move
// that author's books and touch nothing else.
func (s *Server) scheduleMove(ctx context.Context, ids []int64, e *bookEdit) {
	if !e.touchesPath() || s.files == nil {
		return
	}
	for _, id := range ids {
		if err := s.queue.Enqueue(ctx, jobsKindFileMove, itoa(id),
			map[string]int64{"book_id": id}, 300); err != nil {
			s.log.Warn("could not queue file move", "book", id, "err", err)
		}
	}
}
