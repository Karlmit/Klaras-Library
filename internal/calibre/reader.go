// Package calibre reads an existing Calibre library (metadata.db) and
// calibre-web's own state (app.db), and imports both into Klaras Library.
//
// Both source databases are opened strictly read-only and immutable, so an
// import cannot disturb a running calibre-web instance.
package calibre

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver; keeps the binary CGO-free
)

// openReadOnly opens a SQLite file in a way that cannot modify it.
//
// immutable=1 additionally promises SQLite the file will not change while
// open, which suppresses locking entirely -- important because the real
// metadata.db may live on a share we only have read access to, where SQLite
// would otherwise fail trying to create a -wal or -shm file.
func openReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	} else if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory, expected a SQLite file", abs)
	}

	dsn := "file:" + url.PathEscape(abs) + "?mode=ro&immutable=1&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", abs, err)
	}
	// One connection is plenty and avoids re-opening an immutable file.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", abs, err)
	}
	return db, nil
}

// Source is a Calibre library on disk.
type Source struct {
	Root string // library directory containing metadata.db
	db   *sql.DB
}

// OpenSource opens the metadata.db inside a Calibre library directory.
func OpenSource(libraryRoot string) (*Source, error) {
	meta := filepath.Join(libraryRoot, "metadata.db")
	db, err := openReadOnly(meta)
	if err != nil {
		return nil, err
	}
	return &Source{Root: libraryRoot, db: db}, nil
}

// Close releases the source database.
func (s *Source) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Stats summarises a Calibre library without importing it.
type Stats struct {
	Books        int64
	Authors      int64
	Series       int64
	Tags         int64
	Publishers   int64
	Files        int64
	Comments     int64
	Identifiers  int64
	CustomCols   int64
	FormatCounts map[string]int64
	Languages    map[string]int64
}

// Stat reports what the library contains. Used by --dry-run and by the
// pre-flight summary printed before any write happens.
func (s *Source) Stat() (*Stats, error) {
	st := &Stats{
		FormatCounts: map[string]int64{},
		Languages:    map[string]int64{},
	}
	scalars := []struct {
		q   string
		dst *int64
	}{
		{"SELECT count(*) FROM books", &st.Books},
		{"SELECT count(*) FROM authors", &st.Authors},
		{"SELECT count(*) FROM series", &st.Series},
		{"SELECT count(*) FROM tags", &st.Tags},
		{"SELECT count(*) FROM publishers", &st.Publishers},
		{"SELECT count(*) FROM data", &st.Files},
		{"SELECT count(*) FROM comments", &st.Comments},
		{"SELECT count(*) FROM identifiers", &st.Identifiers},
		{"SELECT count(*) FROM custom_columns", &st.CustomCols},
	}
	for _, sc := range scalars {
		if err := s.db.QueryRow(sc.q).Scan(sc.dst); err != nil {
			return nil, fmt.Errorf("%s: %w", sc.q, err)
		}
	}

	rows, err := s.db.Query(`SELECT format, count(*) FROM data GROUP BY format`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f string
		var n int64
		if err := rows.Scan(&f, &n); err != nil {
			return nil, err
		}
		st.FormatCounts[f] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	lrows, err := s.db.Query(`
		SELECT l.lang_code, count(*)
		FROM books_languages_link bll
		JOIN languages l ON l.id = bll.lang_code
		GROUP BY l.lang_code`)
	if err != nil {
		return nil, err
	}
	defer lrows.Close()
	for lrows.Next() {
		var code string
		var n int64
		if err := lrows.Scan(&code, &n); err != nil {
			return nil, err
		}
		st.Languages[code] = n
	}
	return st, lrows.Err()
}
