package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// handleAuthors lists every author with a book count.
func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	out, err := s.lib.Authors(r.Context())
	if err != nil {
		s.fail(w, r, err, "list authors")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authors": out})
}

// handlePortraitStatus reports how far the portrait sweep has got.
func (s *Server) handlePortraitStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.lib.PortraitStatus(r.Context())
	if err != nil {
		s.fail(w, r, err, "portrait status")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSeries lists every series with a book count and a few cover ids.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	out, err := s.lib.Series(r.Context())
	if err != nil {
		s.fail(w, r, err, "list series")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": out})
}

// handleAuthorPortrait serves an author's picture, fetching it the first time.
//
// 404 is an ordinary answer here rather than a failure: most authors in a
// library this size are not on Wikidata, and the grid draws its own initials
// when there is no photograph.
func (s *Server) handleAuthorPortrait(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad author id")
		return
	}
	path, err := s.lib.PortraitPath(r.Context(), s.cfg.CacheDir, id)
	if errors.Is(err, library.ErrNoPortrait) {
		// Cached: the miss is permanent until something re-runs the lookup, so
		// the browser should not ask again on the next scroll either.
		w.Header().Set("Cache-Control", "private, max-age=86400")
		writeErr(w, http.StatusNotFound, "no portrait")
		return
	}
	if err != nil {
		s.fail(w, r, err, "author portrait")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}

// handleMergeTags renames a category, folding it into an existing one if the
// new name is already in use.
//
// The library has 1,113 categories, many of them the same thing written twice
// -- "F" on 6,706 books and "Fiction" on others. Fixing that by selecting every
// book and editing them one bulk at a time is the long way round: the category
// itself is what is wrong, so it is the category that gets renamed.
func (s *Server) handleMergeTags(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From []string `json:"from"`
		To   string   `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	to := strings.TrimSpace(in.To)
	from := make([]string, 0, len(in.From))
	for _, f := range in.From {
		if f = strings.TrimSpace(f); f != "" && f != to {
			from = append(from, f)
		}
	}
	if len(from) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing to rename")
		return
	}
	if to == "" {
		// Renaming to nothing is how a category is deleted, which is a real
		// need for the junk ones -- but it must be asked for explicitly.
		if !queryBool(r, "delete") {
			writeErr(w, http.StatusBadRequest, "give a new name, or pass delete=true to remove it")
			return
		}
	}

	n, err := s.lib.MergeTags(r.Context(), from, to)
	if err != nil {
		s.fail(w, r, err, "merge categories")
		return
	}
	// The category list is a materialised view refreshed every half minute, so
	// without this the panel reloads and still shows the names it has just
	// merged away -- which reads as the merge having failed. A refresh here
	// costs a moment and makes the answer honest.
	if _, err := s.lib.RefreshFacets(r.Context(), true); err != nil {
		s.log.Warn("facet refresh after merge failed", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "books": n})
}

// handleAuthor serves one author's page.
func (s *Server) handleAuthor(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad author id")
		return
	}
	a, err := s.lib.Author(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "author")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// handleAuthorPortraitUpload replaces an author's picture with an uploaded file.
func (s *Server) handleAuthorPortraitUpload(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad author id")
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the upload")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no file in the upload")
		return
	}
	defer f.Close()

	if err := s.lib.SetPortrait(r.Context(), s.cfg.CacheDir, id, f, "uploaded"); err != nil {
		if errors.Is(err, library.ErrNotAnImage) {
			writeErr(w, http.StatusBadRequest, "that file could not be read as an image")
			return
		}
		s.fail(w, r, err, "set portrait")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "portrait set"})
}

// handleAuthorPortraitFetch takes a picture from a URL, with the same guard as
// book covers: the server opens whatever address it is given, so it must not be
// able to reach anything inside this network.
func (s *Server) handleAuthorPortraitFetch(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad author id")
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, "that does not look like an image address")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "that address could not be read")
		return
	}
	req.Header.Set("User-Agent", "Klaras-Library/1 (+https://github.com/Karlmit/Klaras-Library)")
	req.Header.Set("Accept", "image/*")

	res, err := coverFetchClient.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "is not a public address") {
			writeErr(w, http.StatusBadRequest,
				"that address is inside this network, so it will not be fetched")
			return
		}
		writeErr(w, http.StatusBadGateway, "that picture could not be downloaded")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, "that address returned an error")
		return
	}

	if err := s.lib.SetPortrait(r.Context(), s.cfg.CacheDir, id,
		io.LimitReader(res.Body, 8<<20), u.String()); err != nil {
		if errors.Is(err, library.ErrNotAnImage) {
			writeErr(w, http.StatusBadRequest, "that address is not an image")
			return
		}
		s.fail(w, r, err, "set portrait")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "portrait set"})
}

// handleAuthorPortraitDelete removes an author's picture.
func (s *Server) handleAuthorPortraitDelete(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad author id")
		return
	}
	if err := s.lib.ClearPortrait(r.Context(), s.cfg.CacheDir, id); err != nil {
		s.fail(w, r, err, "clear portrait")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "portrait cleared"})
}

// handleAuthorPortraitLookup searches again for an author whose sweep found
// nothing -- useful once a misspelled name has been corrected.
func (s *Server) handleAuthorPortraitLookup(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad author id")
		return
	}
	err = s.lib.LookUpPortrait(r.Context(), s.cfg.CacheDir, id)
	if errors.Is(err, library.ErrNoPortrait) {
		writeErr(w, http.StatusNotFound, "nothing found for that name")
		return
	}
	if err != nil {
		s.fail(w, r, err, "look up portrait")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "portrait found"})
}
