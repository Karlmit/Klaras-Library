package kobo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Karlmit/Klaras-Library/internal/covers"
)

// handleMetadata returns one book's metadata. The device asks for this after a
// sync when it wants to refresh a single title.
func (h *Handler) handleMetadata(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	u := userOf(r)

	books, _, err := h.engine.changedBooks(r.Context(), u.ID, epoch, 100000)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for i := range books {
		if books[i].UUID == uuid {
			writeJSON(w, http.StatusOK, []BookMetadata{h.metadataFor(r, &books[i])})
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// handleGetState returns stored reading progress for one book.
func (h *Handler) handleGetState(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	u := userOf(r)

	bookID, _, err := h.bookByUUID(r.Context(), uuid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var s readingStateRow
	s.BookUUID = uuid
	err = h.pool.QueryRow(r.Context(), `
		SELECT status, progress_percent, content_source_progress_percent,
		       location_value, location_type, location_source,
		       spent_reading_minutes, remaining_time_minutes,
		       times_started_reading, last_time_started_reading,
		       last_time_finished, priority_timestamp, last_modified
		FROM reading_state WHERE user_id=$1 AND book_id=$2`, u.ID, bookID).
		Scan(&s.Status, &s.Progress, &s.SrcProgress, &s.LocValue, &s.LocType,
			&s.LocSource, &s.Spent, &s.Remaining, &s.TimesStarted,
			&s.LastStarted, &s.LastFinished, &s.Priority, &s.Modified)
	if err != nil {
		// No stored progress is normal, not an error: hand back a fresh state
		// so the device has something well-formed to start from.
		now := time.Now().UTC()
		s = readingStateRow{BookUUID: uuid, Status: "ReadyToRead", Priority: now, Modified: now}
	}
	writeJSON(w, http.StatusOK, []ReadingState{readingStateJSON(&s)})
}

// putStateRequest is what the device sends when progress changes.
type putStateRequest struct {
	ReadingStates []struct {
		EntitlementId   string `json:"EntitlementId"`
		CurrentBookmark *struct {
			ContentSourceProgressPercent *int `json:"ContentSourceProgressPercent"`
			ProgressPercent              *int `json:"ProgressPercent"`
			Location                     *struct {
				Source string `json:"Source"`
				Type   string `json:"Type"`
				Value  string `json:"Value"`
			} `json:"Location"`
		} `json:"CurrentBookmark"`
		Statistics *struct {
			RemainingTimeMinutes *int `json:"RemainingTimeMinutes"`
			SpentReadingMinutes  *int `json:"SpentReadingMinutes"`
		} `json:"Statistics"`
		StatusInfo *struct {
			Status string `json:"Status"`
		} `json:"StatusInfo"`
	} `json:"ReadingStates"`
}

// handlePutState stores progress coming back from the device.
func (h *Handler) handlePutState(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	u := userOf(r)

	bookID, _, err := h.bookByUUID(r.Context(), uuid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var req putStateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, rs := range req.ReadingStates {
		status := "ReadyToRead"
		if rs.StatusInfo != nil && rs.StatusInfo.Status != "" {
			status = rs.StatusInfo.Status
		}
		var progress, srcProgress *int
		var locVal, locType, locSrc *string
		if rs.CurrentBookmark != nil {
			progress = rs.CurrentBookmark.ProgressPercent
			srcProgress = rs.CurrentBookmark.ContentSourceProgressPercent
			if l := rs.CurrentBookmark.Location; l != nil {
				locVal, locType, locSrc = &l.Value, &l.Type, &l.Source
			}
		}
		var spent, remaining *int
		if rs.Statistics != nil {
			spent = rs.Statistics.SpentReadingMinutes
			remaining = rs.Statistics.RemainingTimeMinutes
		}

		// COALESCE on update: the device often sends a partial state, and
		// overwriting an existing bookmark with a null would lose the reader's
		// place.
		if _, err := h.pool.Exec(r.Context(), `
			INSERT INTO reading_state (
				user_id, book_id, status, progress_percent,
				content_source_progress_percent, location_value, location_type,
				location_source, spent_reading_minutes, remaining_time_minutes,
				times_started_reading, last_time_started_reading, last_modified)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
			        CASE WHEN $3 = 'Reading' THEN 1 ELSE 0 END,
			        CASE WHEN $3 = 'Reading' THEN now() ELSE NULL END,
			        now())
			ON CONFLICT (user_id, book_id) DO UPDATE SET
				status                          = EXCLUDED.status,
				progress_percent                = COALESCE(EXCLUDED.progress_percent, reading_state.progress_percent),
				content_source_progress_percent = COALESCE(EXCLUDED.content_source_progress_percent, reading_state.content_source_progress_percent),
				location_value                  = COALESCE(EXCLUDED.location_value, reading_state.location_value),
				location_type                   = COALESCE(EXCLUDED.location_type, reading_state.location_type),
				location_source                 = COALESCE(EXCLUDED.location_source, reading_state.location_source),
				spent_reading_minutes           = COALESCE(EXCLUDED.spent_reading_minutes, reading_state.spent_reading_minutes),
				remaining_time_minutes          = COALESCE(EXCLUDED.remaining_time_minutes, reading_state.remaining_time_minutes),
				times_started_reading           = reading_state.times_started_reading +
					CASE WHEN EXCLUDED.status = 'Reading' AND reading_state.status <> 'Reading' THEN 1 ELSE 0 END,
				last_time_started_reading       = CASE WHEN EXCLUDED.status = 'Reading' AND reading_state.status <> 'Reading'
				                                       THEN now() ELSE reading_state.last_time_started_reading END,
				last_time_finished              = CASE WHEN EXCLUDED.status = 'Finished'
				                                       THEN now() ELSE reading_state.last_time_finished END,
				last_modified                   = now()`,
			u.ID, bookID, status, progress, srcProgress, locVal, locType, locSrc,
			spent, remaining); err != nil {
			h.log.Error("kobo: store reading state", "err", err, "book", bookID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"RequestResult": "Success",
		"UpdateResults": []map[string]any{{
			"CurrentBookmarkResult": map[string]string{"Result": "Success"},
			"EntitlementId":         uuid,
			"StatisticsResult":      map[string]string{"Result": "Success"},
			"StatusInfoResult":      map[string]string{"Result": "Success"},
		}},
	})
}

// handleArchive marks a book archived for this user, which is how the device
// asks for it to be removed from the shelf without deleting it.
func (h *Handler) handleArchive(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	u := userOf(r)

	bookID, _, err := h.bookByUUID(r.Context(), uuid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO kobo_archived (user_id, book_id) VALUES ($1,$2)
		ON CONFLICT DO NOTHING`, u.ID, bookID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleDownload streams a book file to the device.
func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	format := strings.ToUpper(chi.URLParam(r, "format"))

	var bookID int64
	if _, err := fmtSscan(idStr, &bookID); err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	var path, uuid string
	if err := h.pool.QueryRow(r.Context(),
		`SELECT path, uuid::text FROM books WHERE id=$1`, bookID).Scan(&path, &uuid); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var filename string
	err := h.pool.QueryRow(r.Context(),
		`SELECT filename FROM book_files WHERE book_id=$1 AND format=$2`, bookID, format).Scan(&filename)

	full := ""
	switch {
	case err == nil:
		full = filepath.Join(h.libraryRoot, path, filename)
	case format == "KEPUB":
		// No KEPUB on disk: serve the one we converted, if it is ready.
		var epubName string
		if e := h.pool.QueryRow(r.Context(),
			`SELECT filename FROM book_files WHERE book_id=$1 AND format='EPUB'`, bookID).
			Scan(&epubName); e != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		src := filepath.Join(h.libraryRoot, path, epubName)
		cached, ok := h.kepub.Cached(uuid, src)
		if !ok {
			// Convert inline as a last resort. This only happens if the device
			// asks for a format the sync response did not advertise.
			h.log.Warn("converting kepub during a download", "book", bookID)
			cached, err = h.kepub.Convert(r.Context(), uuid, src)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
		}
		full = cached
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	f, err := os.Open(full)
	if err != nil {
		h.log.Error("kobo download: open", "path", full, "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	name := filepath.Base(full)
	w.Header().Set("Content-Type", "application/epub+zip")
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(name))
	// ServeContent handles range requests, which the device uses to resume an
	// interrupted download over a flaky wifi link.
	http.ServeContent(w, r, name, st.ModTime(), f)

	// Fetching the file is the only hard evidence we ever get that the device
	// actually holds a book. A sync token proves it read a response, which is
	// weaker: it acknowledged the ChangedEntitlements that left Klara's
	// collection empty. Recorded after serving so a failed transfer does not
	// count, and best-effort because the bytes are already on their way.
	if err := h.engine.confirmDownloaded(context.WithoutCancel(r.Context()),
		userOf(r).ID, bookID); err != nil {
		h.log.Warn("kobo download: confirm", "book", bookID, "err", err)
	}
}

// handleImage serves a cover at whatever size the device asked for.
func (h *Handler) handleImage(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	width := parseIntDefault(chi.URLParam(r, "width"), 400)

	var path string
	if err := h.pool.QueryRow(r.Context(),
		`SELECT path FROM books WHERE uuid=$1`, uuid).Scan(&path); err != nil {
		h.servePlaceholder(w, width)
		return
	}

	// Pick the nearest pre-generated size at or above what was asked for,
	// rather than resizing per request.
	size := covers.Sizes[len(covers.Sizes)-1]
	for _, s := range covers.Sizes {
		if s.Width >= width {
			size = s
			break
		}
	}
	p, ok := h.covers.ThumbPath(uuid, size.Name)
	if !ok {
		if err := h.covers.Generate(path, uuid); err != nil {
			h.servePlaceholder(w, width)
			return
		}
		p, ok = h.covers.ThumbPath(uuid, size.Name)
		if !ok {
			h.servePlaceholder(w, width)
			return
		}
	}

	f, err := os.Open(p)
	if err != nil {
		h.servePlaceholder(w, width)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "cover.jpg", st.ModTime(), f)
}

func (h *Handler) servePlaceholder(w http.ResponseWriter, width int) {
	w.Header().Set("Content-Type", "image/jpeg")
	_ = covers.Placeholder(w, width)
}

// handleInitialization tells the device which endpoints to use.
//
// Every URL here points back at us, which is what redirects the device away
// from the Kobo store and at this library.
func (h *Handler) handleInitialization(w http.ResponseWriter, r *http.Request) {
	// Start from the complete native table. The device REPLACES its stored
	// resource list with whatever comes back, so returning a subset does not
	// mean "leave the rest alone" -- it means "blank the rest", which is how
	// a reader ends up with an empty account_page, autocomplete, categories
	// and every other endpoint it needs, and stops syncing.
	res := nativeKoboResources()

	// When proxying, prefer the live table so it tracks whatever Kobo changes,
	// falling back to ours if the store is slow or unreachable.
	if h.proxyStore {
		if live := h.fetchStoreResources(r); live != nil {
			for k, v := range live {
				res[k] = v
			}
		}
	}

	// Then point only the image URLs at us, so covers come from this library.
	if base := strings.TrimRight(h.externalURL, "/"); base != "" {
		prefix := base + "/kobo/" + tokenOf(r)
		res["image_host"] = base
		res["image_url_template"] = prefix + "/{ImageId}/{Width}/{Height}/false/image.jpg"
		res["image_url_quality_template"] = prefix +
			"/{ImageId}/{Width}/{Height}/{Quality}/{IsGreyscale}/image.jpg"
	} else {
		// No external URL configured: leave Kobo's own CDN in place rather
		// than handing the device an address it will store and cannot reach.
		h.log.Warn("KLARAS_EXTERNAL_URL is not set; leaving Kobo's image CDN in the " +
			"device's resource table, so covers will not come from this library")
	}

	// calibre-web sends this alongside the resource table and the device has
	// only ever been seen to work with it present. "e30=" is base64 "{}": an
	// empty API token, which is what a self-hosted library legitimately has.
	setKoboHeader(w.Header(), "x-kobo-apitoken", "e30=")
	writeJSON(w, http.StatusOK, map[string]any{"Resources": res})
}

// fetchStoreResources reads the live resource table from the Kobo store.
func (h *Handler) fetchStoreResources(r *http.Request) map[string]any {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		koboStoreHost+"/v1/initialization", nil)
	if err != nil {
		return nil
	}
	for k, vs := range r.Header {
		if isHopByHop(k) || strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := proxyClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		h.log.Debug("could not read the live Kobo resource table; using the built-in one")
		return nil
	}
	defer resp.Body.Close()

	var body struct {
		Resources map[string]any `json:"Resources"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil
	}
	return body.Resources
}

// handleFallback answers the many endpoints a device probes that we do not
// implement.
//
// Returning 404 is not neutral here: the firmware retries and surfaces sync
// errors to the user. An empty 200 keeps it happy. Optionally the request is
// forwarded to the real store so the device's shop still works.
func (h *Handler) handleFallback(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore {
		h.proxyToKoboStore(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

var errUnsupported = errors.New("unsupported")

func fmtSscan(s string, out *int64) (int, error) {
	var n int64
	if s == "" {
		return 0, errUnsupported
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errUnsupported
		}
		n = n*10 + int64(c-'0')
	}
	*out = n
	return 1, nil
}
