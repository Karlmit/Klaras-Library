package library

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a book does not exist.
var ErrNotFound = errors.New("not found")

// BookFile is one file on disk.
type BookFile struct {
	Format string `json:"format"`
	Name   string `json:"filename"`
	Size   int64  `json:"size_bytes"`
}

// Identifier is an external id such as an ISBN.
type Identifier struct {
	Scheme string `json:"scheme"`
	Value  string `json:"value"`
}

// Book is the full detail view.
type Book struct {
	ID          int64    `json:"id"`
	UUID        string   `json:"uuid"`
	Title       string   `json:"title"`
	TitleSort   string   `json:"title_sort"`
	Authors     []string `json:"authors"`
	AuthorSort  string   `json:"author_sort"`
	Description *string  `json:"description,omitempty"`
	Series      *string  `json:"series,omitempty"`
	SeriesIndex *float64 `json:"series_index,omitempty"`
	Publisher   *string  `json:"publisher,omitempty"`
	PubDate     *string  `json:"pubdate,omitempty"`
	Rating      *int16   `json:"rating,omitempty"`
	Tags        []string `json:"tags"`
	Languages   []string `json:"languages"`
	Path        string   `json:"path"`
	HasCover    bool     `json:"has_cover"`
	// CoverW/CoverH are the real dimensions of the file on disk, filled in by
	// the handler. Zero when there is no cover or it could not be read.
	CoverW        int          `json:"cover_w,omitempty"`
	CoverH        int          `json:"cover_h,omitempty"`
	Files         []BookFile   `json:"files"`
	Identifiers   []Identifier `json:"identifiers"`
	NeedsReview   bool         `json:"needs_review"`
	Adult         bool         `json:"adult"`
	AdultReason   string       `json:"adult_reason,omitempty"`
	ReviewReasons []string     `json:"review_reasons,omitempty"`
	AddedAt       string       `json:"added_at"`
	UpdatedAt     string       `json:"updated_at"`
	Shelves       []ShelfRef   `json:"shelves,omitempty"`
}

// ShelfRef names a shelf a book sits on.
type ShelfRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	KoboSync bool   `json:"kobo_sync"`
}

// GetBook loads one book in full.
func (s *Store) GetBook(ctx context.Context, id int64, userID int64) (*Book, error) {
	var b Book
	var added, updated time.Time
	var pub *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT b.id, b.uuid, b.title, b.title_sort, b.author_names, b.author_sort,
		       b.description, b.series_name, b.series_index, p.name, b.pubdate,
		       b.rating, b.tag_names, b.languages, b.path, b.has_cover,
		       b.needs_review, b.review_reasons, b.added_at, b.updated_at,
		       b.adult, COALESCE(b.adult_reason,'')
		FROM books b
		LEFT JOIN publishers p ON p.id = b.publisher_id
		WHERE b.id = $1`, id).
		Scan(&b.ID, &b.UUID, &b.Title, &b.TitleSort, &b.Authors, &b.AuthorSort,
			&b.Description, &b.Series, &b.SeriesIndex, &b.Publisher, &pub,
			&b.Rating, &b.Tags, &b.Languages, &b.Path, &b.HasCover,
			&b.NeedsReview, &b.ReviewReasons, &added, &updated,
			&b.Adult, &b.AdultReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.AddedAt = added.UTC().Format(time.RFC3339)
	b.UpdatedAt = updated.UTC().Format(time.RFC3339)
	if pub != nil {
		d := pub.Format("2006-01-02")
		b.PubDate = &d
	}
	b.Files = []BookFile{}
	b.Identifiers = []Identifier{}

	frows, err := s.pool.Query(ctx,
		`SELECT format, filename, size_bytes FROM book_files WHERE book_id=$1 ORDER BY format`, id)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		var f BookFile
		if err := frows.Scan(&f.Format, &f.Name, &f.Size); err != nil {
			return nil, err
		}
		b.Files = append(b.Files, f)
	}
	if err := frows.Err(); err != nil {
		return nil, err
	}

	irows, err := s.pool.Query(ctx,
		`SELECT scheme, value FROM identifiers WHERE book_id=$1 ORDER BY scheme`, id)
	if err != nil {
		return nil, err
	}
	defer irows.Close()
	for irows.Next() {
		var i Identifier
		if err := irows.Scan(&i.Scheme, &i.Value); err != nil {
			return nil, err
		}
		b.Identifiers = append(b.Identifiers, i)
	}
	if err := irows.Err(); err != nil {
		return nil, err
	}

	if userID > 0 {
		srows, err := s.pool.Query(ctx, `
			SELECT s.id, s.name, s.kobo_sync
			FROM shelves s JOIN shelf_books sb ON sb.shelf_id = s.id
			WHERE sb.book_id = $1 AND (s.user_id = $2 OR s.is_public)
			ORDER BY s.name`, id, userID)
		if err != nil {
			return nil, err
		}
		defer srows.Close()
		for srows.Next() {
			var sh ShelfRef
			if err := srows.Scan(&sh.ID, &sh.Name, &sh.KoboSync); err != nil {
				return nil, err
			}
			b.Shelves = append(b.Shelves, sh)
		}
		if err := srows.Err(); err != nil {
			return nil, err
		}
	}
	return &b, nil
}

// BookPathInfo is the minimum needed to locate files on disk.
type BookPathInfo struct {
	ID    int64
	UUID  string
	Title string
	Path  string
	Files []BookFile
}

// PathInfo loads just the on-disk location of a book.
func (s *Store) PathInfo(ctx context.Context, id int64) (*BookPathInfo, error) {
	var p BookPathInfo
	err := s.pool.QueryRow(ctx,
		`SELECT id, uuid, title, path FROM books WHERE id=$1`, id).
		Scan(&p.ID, &p.UUID, &p.Title, &p.Path)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT format, filename, size_bytes FROM book_files WHERE book_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f BookFile
		if err := rows.Scan(&f.Format, &f.Name, &f.Size); err != nil {
			return nil, err
		}
		p.Files = append(p.Files, f)
	}
	return &p, rows.Err()
}

// PathInfoByUUID resolves a book by its Calibre UUID, which is how Kobo
// devices refer to books.
func (s *Store) PathInfoByUUID(ctx context.Context, uuid string) (*BookPathInfo, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM books WHERE uuid=$1`, uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.PathInfo(ctx, id)
}
