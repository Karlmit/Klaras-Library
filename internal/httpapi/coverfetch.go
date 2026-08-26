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

// A cover may be fetched from anywhere on the public internet.
//
// This endpoint takes a URL and asks the server to open it, which is a
// server-side request forgery waiting to happen: unchecked, "url" could name
// the Docker network, the Unraid host, or a cloud metadata endpoint, and the
// reply would be written into a book as a picture.
//
// It used to be guarded by a list of provider hostnames. That list cannot
// survive pasting a cover URL from an arbitrary shop or archive, which is the
// point of the feature -- and it was never the right control anyway. What must
// not happen is reaching something internal, and that is a property of the
// address, not the name. safeDialer enforces it at connect time, on every
// redirect hop, against the IP actually being dialled. See safedial.go.
//
// What remains here: only http and https, a bounded number of hops, a size
// limit, and a decode -- the bytes have to be an image before they are kept.
var coverFetchClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &http.Transport{DialContext: safeDialer.DialContext},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("too many redirects")
		}
		// The dialer covers where a hop goes; this covers how. Without it a
		// redirect to file:// or another scheme leaves the guard behind.
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing to follow a %s redirect", req.URL.Scheme)
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
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, "that does not look like an image address")
		return
	}

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
		s.log.Warn("cover fetch failed", "book", id, "url", u.String(), "err", err)
		// The dialer refuses internal addresses, and that refusal is worth
		// naming: it is a rule, not a network problem to retry.
		if strings.Contains(err.Error(), "is not a public address") {
			writeErr(w, http.StatusBadRequest,
				"that address is inside this network, so it will not be fetched")
			return
		}
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
