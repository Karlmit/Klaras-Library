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

	// Both providers run together unless one is named. Narrowing cannot surface
	// anything the default misses -- it is for reading one source on its own.
	set := s.providers
	if name := strings.TrimSpace(q.Get("provider")); name != "" && !strings.EqualFold(name, "all") {
		set = set.Only(name)
		if len(set.Names()) == 0 {
			writeErr(w, http.StatusBadRequest, "no provider by that name")
			return
		}
	}

	results, sources := set.SearchWithStatus(r.Context(), query, queryInt(r, "limit", 10))
	writeJSON(w, http.StatusOK, map[string]any{
		// Echo the query back: the panel prefills these from the book, and
		// someone retrying a failed lookup needs to see what was actually asked
		// before they can change it.
		"query":     map[string]string{"title": query.Title, "author": query.Author, "isbn": query.ISBN},
		"providers": s.providers.Names(),
		"searched":  set.Names(),
		// What each source did. Without it, a provider that is out of quota is
		// indistinguishable from one that has never heard of the book.
		"sources": sources,
		"results": results,
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
