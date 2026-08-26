package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Karlmit/Klaras-Library/internal/covers"
	"github.com/Karlmit/Klaras-Library/internal/jobs"
)

// coverHosts are the only places a cover may be fetched from.
//
// This endpoint takes a URL and asks the server to open it, which is a
// server-side request forgery waiting to happen: without a list, "cover_url"
// could name the Docker network, the Unraid host, or a cloud metadata endpoint,
// and the reply would be written into a book as a picture. The list is not a
// convenience -- it is the whole security of the thing.
//
// These are the hosts the bundled providers actually return.
var coverHosts = map[string]bool{
	"books.google.com":            true,
	"books.googleusercontent.com": true,
	"covers.openlibrary.org":      true,
	// Open Library's cover URLs are a 302 to the Internet Archive, which is
	// where the file actually lives.
	"archive.org": true,
}

// coverHostAllowed also accepts the Archive's numbered delivery nodes, where a
// download redirect finally lands: ia800304.us.archive.org and the like.
// Matching the suffix is the only workable option; there are hundreds and they
// change.
func coverHostAllowed(host string) bool {
	return coverHosts[host] || strings.HasSuffix(host, ".archive.org")
}

var coverFetchClient = &http.Client{
	Timeout: 20 * time.Second,
	// The allow-list has to hold on every hop, not only the first.
	//
	// Checking just the submitted URL is a well-worn way to be led somewhere
	// else: a permitted host answers 302 naming an internal address, and the
	// client follows it carrying the server's own network position. Open
	// Library legitimately redirects twice, so refusing redirects outright is
	// not an option either.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("too many redirects")
		}
		if !coverHostAllowed(req.URL.Hostname()) {
			return fmt.Errorf("redirected to %s, which is not a known cover source",
				req.URL.Hostname())
		}
		return nil
	},
}

// handleFetchCover pulls a cover from a provider and makes it the book's own.
func (s *Server) handleFetchCover(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
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
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !coverHostAllowed(u.Hostname()) {
		writeErr(w, http.StatusBadRequest, "that address is not a known cover source")
		return
	}
	// Providers hand out http links for images they serve over https too.
	u.Scheme = "https"

	info, err := s.lib.PathInfo(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "cover lookup")
		return
	}
	dir, err := s.files.Abs(info.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "that address could not be read")
		return
	}
	// Some hosts, the Internet Archive among them, answer the default Go agent
	// with something other than the image.
	req.Header.Set("User-Agent", "Klaras-Library/1 (+https://github.com/Karlmit/Klaras-Library)")
	req.Header.Set("Accept", "image/*")

	res, err := coverFetchClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "the cover could not be downloaded")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, "the cover source returned an error")
		return
	}

	// WriteSource decodes and re-encodes, so anything that is not really an
	// image fails here rather than in every thumbnail worker later. It also
	// writes the file, and the two failures need telling apart: "that is not an
	// image" sends someone looking at the cover source when the real problem is
	// a library mounted read-only, which is a fault on this side.
	if err := covers.WriteSource(dir, io.LimitReader(res.Body, 16<<20)); err != nil {
		s.log.Warn("cover fetch failed", "book", id, "url", u.String(), "err", err)
		if strings.Contains(err.Error(), "not a readable image") {
			writeErr(w, http.StatusBadRequest, "that file could not be read as an image")
		} else {
			writeErr(w, http.StatusInternalServerError,
				"the cover downloaded but could not be saved; check the library is writable")
		}
		return
	}
	if _, err := s.db.Pool.Exec(r.Context(),
		`UPDATE books SET has_cover = true WHERE id = $1`, id); err != nil {
		s.fail(w, r, err, "mark cover")
		return
	}
	s.covers.Invalidate(info.UUID)
	if err := s.covers.Generate(info.Path, info.UUID); err != nil {
		_ = s.queue.Enqueue(r.Context(), jobs.KindThumbnail, info.UUID,
			covers.ThumbnailPayload{BookID: id, UUID: info.UUID, Path: info.Path}, 10)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cover replaced"})
}
