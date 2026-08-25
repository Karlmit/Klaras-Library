package httpapi

import (
	"net/http"
	"strings"

	"github.com/Karlmit/Klaras-Library/internal/provider"
)

// handleMetadataSearch looks a book up in the external providers.
//
// Results are returned for review, never applied automatically: providers
// routinely return an English edition of a different book as their top hit,
// and silently overwriting good metadata with that is worse than doing nothing.
func (s *Server) handleMetadataSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := provider.Query{
		Title:  strings.TrimSpace(q.Get("title")),
		Author: strings.TrimSpace(q.Get("author")),
		ISBN:   strings.TrimSpace(q.Get("isbn")),
		Lang:   strings.TrimSpace(q.Get("lang")),
	}

	// Called with a book id, prefill from what we already hold.
	if idStr := q.Get("book"); idStr != "" {
		id, err := parseInt64(idStr)
		if err == nil {
			if b, err := s.lib.GetBook(r.Context(), id, 0); err == nil {
				if query.Title == "" {
					query.Title = b.Title
				}
				if query.Author == "" && len(b.Authors) > 0 {
					query.Author = b.Authors[0]
				}
				if query.Lang == "" && len(b.Languages) > 0 {
					query.Lang = b.Languages[0]
				}
				for _, i := range b.Identifiers {
					if i.Scheme == "isbn" && query.ISBN == "" {
						query.ISBN = i.Value
					}
				}
			}
		}
	}

	if query.Title == "" && query.ISBN == "" && query.Author == "" {
		writeErr(w, http.StatusBadRequest, "give a title, author, isbn or book id")
		return
	}

	results := s.providers.Search(r.Context(), query, queryInt(r, "limit", 10))
	writeJSON(w, http.StatusOK, map[string]any{
		"query":     map[string]string{"title": query.Title, "author": query.Author, "isbn": query.ISBN},
		"providers": s.providers.Names(),
		"results":   results,
	})
}

func parseInt64(s string) (int64, error) {
	var n int64
	var neg bool
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, errBadInt
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

var errBadInt = &parseError{"not an integer"}

type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }
