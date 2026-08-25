package calibre

import (
	"database/sql"
)

// rowSource adapts a SQLite *sql.Rows to pgx.CopyFromSource, so a Calibre
// table streams straight into a Postgres COPY without being buffered in memory.
// The library is small enough to hold in RAM, but streaming keeps peak usage
// flat regardless of library size and is no more code.
type rowSource struct {
	rows *sql.Rows
	// scan reads the current SQLite row and returns the Postgres column values.
	// Returning (nil, nil) skips the row.
	scan func(*sql.Rows) ([]any, error)

	current []any
	err     error
	skipped int64
	copied  int64
}

func newRowSource(rows *sql.Rows, scan func(*sql.Rows) ([]any, error)) *rowSource {
	return &rowSource{rows: rows, scan: scan}
}

func (s *rowSource) Next() bool {
	for s.rows.Next() {
		vals, err := s.scan(s.rows)
		if err != nil {
			s.err = err
			return false
		}
		if vals == nil {
			s.skipped++
			continue // row deliberately dropped (bad data)
		}
		s.current = vals
		s.copied++
		return true
	}
	s.err = s.rows.Err()
	return false
}

func (s *rowSource) Values() ([]any, error) { return s.current, s.err }
func (s *rowSource) Err() error             { return s.err }
