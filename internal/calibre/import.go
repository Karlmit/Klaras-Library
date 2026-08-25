package calibre

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options controls an import run.
type Options struct {
	// DryRun reads and validates everything but rolls back instead of committing.
	DryRun bool
	// Purge empties the destination library tables first. Without it, importing
	// into a non-empty library is refused.
	Purge bool
}

// Result reports what an import did.
type Result struct {
	Authors     int64
	Series      int64
	Publishers  int64
	Tags        int64
	Books       int64
	BookAuthors int64
	BookTags    int64
	Identifiers int64
	Files       int64
	Skipped     map[string]int64
	Issues      []QualityIssue

	// imported records which Calibre book ids actually became rows. Link
	// tables must filter through this rather than through Calibre's own books
	// table, or a book we deliberately skipped drags its links in behind it
	// and trips a foreign key.
	imported  map[int64]struct{}
	Elapsed   time.Duration
	Committed bool
}

// calibreEpoch is the sentinel Calibre writes for "no publication date".
// It appears as 0101-01-01 and must not be imported as a real date.
const calibreEpochYear = 102

// ImportLibrary copies a Calibre library into Klaras Library.
//
// Calibre's primary keys are preserved as our own so the link tables map 1:1
// with no translation, and so a book's id in the UI matches the id in the
// folder name Calibre already wrote on disk. books.calibre_id records the same
// value permanently, which is what distinguishes an imported book from one
// created here later.
func ImportLibrary(ctx context.Context, pool *pgxpool.Pool, src *Source, opts Options, log *slog.Logger) (*Result, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	res, err := importLibraryTx(ctx, tx, src, opts, log)
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		log.Warn("dry run: rolling back", "elapsed", res.Elapsed.Round(time.Millisecond))
		return res, nil // the deferred Rollback does the work
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	res.Committed = true
	return res, nil
}

// importLibraryTx does the work inside a caller-supplied transaction, so the
// library and calibre-web imports can share one. That matters twice over: a
// dry run must let the app.db pass see the books the library pass just wrote,
// and a real run must not leave a committed library with no shelves attached
// if the second half fails.
func importLibraryTx(ctx context.Context, tx pgx.Tx, src *Source, opts Options, log *slog.Logger) (*Result, error) {
	start := time.Now()
	res := &Result{Skipped: map[string]int64{}, imported: map[int64]struct{}{}}

	var existing int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM books`).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 && !opts.Purge {
		return nil, fmt.Errorf("destination already holds %d books; "+
			"re-run with --purge to replace them", existing)
	}
	// Purge unconditionally when asked, not only when books exist. An
	// interrupted earlier attempt can leave authors or tags behind with no
	// books, and those would then collide on their primary keys.
	if opts.Purge {
		if existing > 0 {
			log.Warn("purging destination library", "books", existing)
		}
		if _, err := tx.Exec(ctx, `TRUNCATE books, authors, series, publishers, tags,
			book_authors, book_tags, identifiers, book_files RESTART IDENTITY CASCADE`); err != nil {
			return nil, fmt.Errorf("purge: %w", err)
		}
	}

	// The denormalisation triggers on the link tables fire per statement, which
	// would mean three whole-library refreshes mid-import. Disable them and do
	// one rebuild at the end instead.
	if _, err := tx.Exec(ctx, `
		ALTER TABLE book_authors DISABLE TRIGGER USER;
		ALTER TABLE book_tags    DISABLE TRIGGER USER;`); err != nil {
		return nil, fmt.Errorf("disable triggers: %w", err)
	}

	steps := []struct {
		name string
		fn   func(context.Context, pgx.Tx, *Source, *Result) (int64, error)
	}{
		{"authors", copyAuthors},
		{"series", copySeries},
		{"publishers", copyPublishers},
		{"tags", copyTags},
		{"books", copyBooks},
		{"book_authors", copyBookAuthors},
		{"book_tags", copyBookTags},
		{"identifiers", copyIdentifiers},
		{"book_files", copyBookFiles},
	}
	for _, st := range steps {
		n, err := st.fn(ctx, tx, src, res)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", st.name, err)
		}
		log.Info("copied", "table", st.name, "rows", n)
	}

	if _, err := tx.Exec(ctx, `
		ALTER TABLE book_authors ENABLE TRIGGER USER;
		ALTER TABLE book_tags    ENABLE TRIGGER USER;`); err != nil {
		return nil, fmt.Errorf("enable triggers: %w", err)
	}

	// Identity sequences must clear the highest imported id, or the next
	// insert collides with an existing primary key.
	if err := resetSequences(ctx, tx); err != nil {
		return nil, fmt.Errorf("reset sequences: %w", err)
	}

	log.Info("rebuilding denormalised columns")
	var refreshed int64
	if err := tx.QueryRow(ctx, `SELECT refresh_all_book_denorm()`).Scan(&refreshed); err != nil {
		return nil, fmt.Errorf("denormalise: %w", err)
	}

	// Flag suspect data here, in the middle: AFTER the denormalisation rebuild
	// because several checks read author_names, and BEFORE the timestamp
	// restore because flagging is itself an UPDATE, and every UPDATE that does
	// not set updated_at explicitly gets auto-touched by the books trigger.
	issues, err := flagQualityIssues(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("quality checks: %w", err)
	}
	res.Issues = issues
	for _, i := range issues {
		log.Warn("flagged for review", "reason", i.Reason, "books", i.Count)
	}

	// refresh_all_book_denorm UPDATEs books, and the books trigger bumps
	// updated_at on any update that does not set it explicitly. That would
	// overwrite every book's Calibre last_modified with now(), and Kobo
	// incremental sync keys off updated_at -- so restore the original values.
	restored, err := restoreTimestamps(ctx, tx, src, res)
	if err != nil {
		return nil, fmt.Errorf("restore timestamps: %w", err)
	}
	log.Info("restored calibre last_modified", "books", restored)

	if _, err := tx.Exec(ctx, `ANALYZE`); err != nil {
		return nil, fmt.Errorf("analyze: %w", err)
	}

	res.Elapsed = time.Since(start)
	return res, nil
}

// ImportAll runs the library and calibre-web imports in a single transaction.
//
// This is what the CLI uses. Either everything lands or nothing does, and a
// --dry-run exercises both halves rather than reporting that the library is
// empty because it just rolled its own work back.
func ImportAll(ctx context.Context, pool *pgxpool.Pool, src *Source, app *AppDB,
	opts Options, log *slog.Logger) (*Result, *AppResult, error) {

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	res, err := importLibraryTx(ctx, tx, src, opts, log)
	if err != nil {
		return nil, nil, err
	}

	var ares *AppResult
	if app != nil {
		ares, err = importAppDBTx(ctx, tx, app, opts, log)
		if err != nil {
			return nil, nil, err
		}
	}

	if opts.DryRun {
		log.Warn("dry run: rolling back everything",
			"elapsed", res.Elapsed.Round(time.Millisecond))
		return res, ares, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}
	res.Committed = true
	if ares != nil {
		ares.Committed = true
	}
	return res, ares, nil
}

func resetSequences(ctx context.Context, tx pgx.Tx) error {
	for _, t := range []string{"authors", "series", "publishers", "tags", "books", "book_files"} {
		q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s','id'),
			GREATEST((SELECT COALESCE(max(id),0) FROM %s), 1))`, t, t)
		if _, err := tx.Exec(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	}
	return nil
}

// restoreTimestamps re-applies Calibre's last_modified after the denormalisation
// rebuild. Setting updated_at explicitly is what tells the books trigger not to
// auto-touch it.
func restoreTimestamps(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE import_ts (id bigint PRIMARY KEY, lm timestamptz NOT NULL)
		ON COMMIT DROP`); err != nil {
		return 0, err
	}

	rows, err := src.db.Query(`SELECT id, last_modified FROM books`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	source := newRowSource(rows, func(r *sql.Rows) ([]any, error) {
		var id int64
		var lm sql.NullString
		if err := r.Scan(&id, &lm); err != nil {
			return nil, err
		}
		if _, imported := res.imported[id]; !imported {
			return nil, nil
		}
		t, ok := parseCalibreTime(lm)
		if !ok {
			return nil, nil
		}
		return []any{id, t}, nil
	})
	staged, err := tx.CopyFrom(ctx, pgx.Identifier{"import_ts"}, []string{"id", "lm"}, source)
	if err != nil {
		return 0, err
	}
	if err := source.Err(); err != nil {
		return 0, err
	}
	// Books were imported but not one timestamp parsed: that is a silent
	// data-loss bug (every date in the library would be wrong), not an empty
	// library. Refuse to continue rather than commit it.
	if staged == 0 && len(res.imported) > 0 {
		return 0, fmt.Errorf("none of the %d imported books had a parseable timestamp; "+
			"calibreTimeLayouts does not cover this library's format", len(res.imported))
	}

	tag, err := tx.Exec(ctx, `
		UPDATE books b SET updated_at = t.lm
		FROM import_ts t
		WHERE b.id = t.id AND b.updated_at IS DISTINCT FROM t.lm`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// per-table copiers
// ---------------------------------------------------------------------------

func copyInto(ctx context.Context, tx pgx.Tx, table string, cols []string, rows *sql.Rows,
	scan func(*sql.Rows) ([]any, error)) (int64, int64, error) {
	defer rows.Close()
	source := newRowSource(rows, scan)
	n, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, source)
	if err != nil {
		return 0, source.skipped, err
	}
	if err := source.Err(); err != nil {
		return 0, source.skipped, err
	}
	return n, source.skipped, nil
}

func copyAuthors(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	rows, err := src.db.Query(`SELECT id, name, COALESCE(sort,''), COALESCE(link,'') FROM authors`)
	if err != nil {
		return 0, err
	}
	n, _, err := copyInto(ctx, tx, "authors", []string{"id", "name", "sort", "link"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var id int64
			var name, sort, link string
			if err := r.Scan(&id, &name, &sort, &link); err != nil {
				return nil, err
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, nil
			}
			return []any{id, name, strings.TrimSpace(sort), link}, nil
		})
	res.Authors = n
	return n, err
}

func copySeries(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	rows, err := src.db.Query(`SELECT id, name, COALESCE(sort,'') FROM series`)
	if err != nil {
		return 0, err
	}
	n, _, err := copyInto(ctx, tx, "series", []string{"id", "name", "sort"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var id int64
			var name, sort string
			if err := r.Scan(&id, &name, &sort); err != nil {
				return nil, err
			}
			if strings.TrimSpace(name) == "" {
				return nil, nil
			}
			return []any{id, strings.TrimSpace(name), strings.TrimSpace(sort)}, nil
		})
	res.Series = n
	return n, err
}

func copyPublishers(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	rows, err := src.db.Query(`SELECT id, name FROM publishers`)
	if err != nil {
		return 0, err
	}
	n, _, err := copyInto(ctx, tx, "publishers", []string{"id", "name"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var id int64
			var name string
			if err := r.Scan(&id, &name); err != nil {
				return nil, err
			}
			if strings.TrimSpace(name) == "" {
				return nil, nil
			}
			return []any{id, strings.TrimSpace(name)}, nil
		})
	res.Publishers = n
	return n, err
}

func copyTags(ctx context.Context, tx pgx.Tx, src *Source, res *Result) (int64, error) {
	rows, err := src.db.Query(`SELECT id, name FROM tags`)
	if err != nil {
		return 0, err
	}
	n, _, err := copyInto(ctx, tx, "tags", []string{"id", "name"}, rows,
		func(r *sql.Rows) ([]any, error) {
			var id int64
			var name string
			if err := r.Scan(&id, &name); err != nil {
				return nil, err
			}
			if strings.TrimSpace(name) == "" {
				return nil, nil
			}
			return []any{id, strings.TrimSpace(name)}, nil
		})
	res.Tags = n
	return n, err
}
