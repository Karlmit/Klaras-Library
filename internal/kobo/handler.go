package kobo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Karlmit/Klaras-Library/internal/auth"
	"github.com/Karlmit/Klaras-Library/internal/covers"
	"github.com/Karlmit/Klaras-Library/internal/jobs"
	"github.com/Karlmit/Klaras-Library/internal/kepub"
)

// Handler serves the Kobo endpoints.
type Handler struct {
	pool        *pgxpool.Pool
	engine      *Engine
	auth        *auth.Service
	kepub       *kepub.Service
	covers      *covers.Service
	queue       *jobs.Queue
	limiter     *auth.Limiter
	libraryRoot string
	externalURL string
	proxyStore  bool
	syncLimit   int
	log         *slog.Logger
}

// Deps are the Handler's collaborators.
type Deps struct {
	Pool        *pgxpool.Pool
	Auth        *auth.Service
	Kepub       *kepub.Service
	Covers      *covers.Service
	Queue       *jobs.Queue
	Limiter     *auth.Limiter
	LibraryRoot string
	ExternalURL string
	ProxyStore  bool
	SyncLimit   int
	Log         *slog.Logger
}

// NewHandler builds the Kobo handler.
func NewHandler(d Deps) *Handler {
	if d.SyncLimit <= 0 {
		d.SyncLimit = SyncItemLimit
	}
	// Default rather than tolerate nil. A missing limiter is a missing security
	// control, and silently allowing unlimited token guessing is far worse than
	// the alternative of always having one.
	if d.Limiter == nil {
		d.Limiter = auth.NewLimiter(8, 15*time.Minute, 15*time.Minute)
	}
	return &Handler{
		pool: d.Pool, engine: NewEngine(d.Pool), auth: d.Auth,
		kepub: d.Kepub, covers: d.Covers, queue: d.Queue, limiter: d.Limiter,
		libraryRoot: d.LibraryRoot, externalURL: d.ExternalURL,
		proxyStore: d.ProxyStore, syncLimit: d.SyncLimit, log: d.Log,
	}
}

type koboCtxKey int

const (
	userKey koboCtxKey = iota
	tokenKey
)

// Routes mounts the Kobo API under /kobo/{token}.
//
// The token lives in the URL path rather than a header because that is the only
// thing a Kobo can be configured with: the device is given an api_store URL and
// appends its own paths to it.
func (h *Handler) Routes(r chi.Router) {
	r.Route("/kobo/{token}", func(r chi.Router) {
		// Every Kobo request is logged at info, not debug.
		//
		// A sync is a handful of requests a few times a day, so the volume is
		// nothing, and this is the one flow that cannot be debugged from the
		// outside: the device reports "sync failed" with no detail, and its own
		// logs are awkward to reach. Without this, a server at the default log
		// level shows only the sync itself and hides the download, cover and
		// initialization calls around it -- which is where a failure actually
		// tends to be.
		r.Use(h.logRequests)
		r.Use(h.authenticate)

		r.Get("/v1/library/sync", h.handleSync)
		r.Get("/v1/library/{uuid}/metadata", h.handleMetadata)
		r.Get("/v1/library/{uuid}/state", h.handleGetState)
		r.Put("/v1/library/{uuid}/state", h.handlePutState)
		r.Delete("/v1/library/{uuid}", h.handleArchive)

		r.Get("/v1/initialization", h.handleInitialization)

		// Three store endpoints the device calls before every sync. They are
		// answered specifically rather than with the generic empty object,
		// because calibre-web special-cases exactly these when store proxying
		// is off -- upstream would not carry that code if "{}" were accepted.
		// A device that cannot read one of them abandons the sync it was about
		// to start, which looks identical to a sync failure.
		r.HandleFunc("/v1/analytics/gettests", h.handleGetTests)
		r.Get("/v1/user/loyalty/benefits", h.handleBenefits)
		r.Get("/v1/user/profile", h.handleUserProfile)
		r.Get("/download/{id}/{format}", h.handleDownload)
		r.Get("/{uuid}/{width}/{height}/{greyscale}/image.jpg", h.handleImage)
		r.Get("/{uuid}/{width}/{height}/{quality}/{greyscale}/image.jpg", h.handleImage)

		// Anything unimplemented either goes to the real Kobo store or gets a
		// harmless empty answer. Returning 404 here makes the device retry and
		// eventually show sync errors.
		r.HandleFunc("/*", h.handleFallback)
	})
}

// handleGetTests answers the device's A/B-test probe.
//
// The device echoes its own user key back and expects to see it; calibre-web
// returns this exact shape with proxying off.
func (h *Handler) handleGetTests(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore {
		h.proxyToKoboStore(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Result":  "Success",
		"TestKey": r.Header.Get("X-Kobo-userkey"),
		"Tests":   map[string]any{},
	})
}

// handleBenefits reports that this account has no Kobo loyalty benefits.
func (h *Handler) handleBenefits(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore {
		h.proxyToKoboStore(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"Benefits": map[string]any{}})
}

// handleUserProfile answers the profile probe with an empty object, matching
// calibre-web. It is listed explicitly only so the route exists ahead of the
// catch-all and shows up in the log under its own name.
func (h *Handler) handleUserProfile(w http.ResponseWriter, r *http.Request) {
	h.handleFallback(w, r)
}

// logRequests records every Kobo call, including the ones that fail.
func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// The token is a bearer credential, so it is reduced to a short prefix:
		// enough to tell devices apart in a log, useless to anyone who reads it.
		tok := chi.URLParam(r, "token")
		short := tok
		if len(short) > 8 {
			short = short[:8] + "..."
		}
		path := r.URL.Path
		if tok != "" {
			path = strings.Replace(path, tok, short, 1)
		}

		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", path,
			"status", rec.status,
			"bytes", rec.written,
			"dur_ms", float64(time.Since(start).Microseconds()) / 1000,
			"device", short,
		}
		if rec.status >= 400 {
			h.log.Warn("kobo request failed", attrs...)
		} else {
			h.log.Info("kobo request", attrs...)
		}
	})
}

// statusRecorder captures what was actually sent back.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	n, err := s.ResponseWriter.Write(b)
	s.written += int64(n)
	return n, err
}

// Flush and ReadFrom are forwarded so http.ServeContent keeps its fast paths
// for range requests and large book downloads.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// authenticate resolves the URL token to a user.
func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		if token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Only failures are counted: a paired device polls constantly and
		// legitimately, and must never be locked out for doing so.
		ip := auth.ClientIP(r)
		if ok, wait := h.limiter.Allowed("kobo:" + ip); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
			return
		}
		u, err := h.auth.UserForKoboToken(r.Context(), token)
		if err != nil {
			h.limiter.Fail("kobo:" + ip)
			h.log.Warn("kobo auth failed", "ip", ip, "path", r.URL.Path)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.limiter.Succeed("kobo:" + ip)
		ctx := context.WithValue(r.Context(), userKey, u)
		ctx = context.WithValue(ctx, tokenKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userOf(r *http.Request) *auth.User {
	u, _ := r.Context().Value(userKey).(*auth.User)
	return u
}

func tokenOf(r *http.Request) string {
	t, _ := r.Context().Value(tokenKey).(string)
	return t
}

// handleSync is the endpoint the device polls.
func (h *Handler) handleSync(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	tok := SyncTokenFromRequest(r)
	ctx := r.Context()

	items := make([]any, 0, h.syncLimit)
	contSync := false

	// The store round trip runs alongside the local queries rather than after
	// them: the device is waiting, and Kobo's latency is not ours to control.
	storeCh := make(chan *storeSyncResult, 1)
	go func() { storeCh <- h.syncWithStore(ctx, r, tok) }()

	// --- books -------------------------------------------------------------
	books, more, err := h.engine.changedBooks(ctx, u.ID, tok.BooksLastModified, h.syncLimit)
	if err != nil {
		h.log.Error("kobo sync: changed books", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	contSync = contSync || more

	syncedIDs := make([]int64, 0, len(books))
	newest := tok.BooksLastModified
	for i := range books {
		b := &books[i]
		md := h.metadataFor(r, b)
		ent := h.entitlementFor(b)
		if b.IsNew {
			items = append(items, map[string]any{
				"NewEntitlement": NewEntitlement{BookEntitlement: ent, BookMetadata: md},
			})
		} else {
			items = append(items, map[string]any{
				"ChangedEntitlement": ChangedEntitlement{BookEntitlement: ent, BookMetadata: md},
			})
		}
		syncedIDs = append(syncedIDs, b.ID)
		if b.Changed.After(newest) {
			newest = b.Changed
		}
		// Make sure a KEPUB exists for next time. Queued, never converted
		// inline: the device is waiting on this response.
		h.ensureKepub(ctx, b)
	}

	// --- books that left every synced shelf ---------------------------------
	if len(items) < h.syncLimit {
		removed, err := h.engine.removedBooks(ctx, u.ID, tok.ArchiveLastModified, h.syncLimit-len(items))
		if err != nil {
			h.log.Error("kobo sync: removed books", "err", err)
		} else {
			for _, uuid := range removed {
				items = append(items, map[string]any{
					"ChangedEntitlement": ChangedEntitlement{
						BookEntitlement: BookEntitlement{
							Id: uuid, RevisionId: uuid, CrossRevisionId: uuid,
							IsRemoved: true, IsHiddenFromArchive: true,
							Status: "Active", Accessibility: "Full",
							OriginCategory: "Imported",
							Created:        koboTime(time.Now()),
							LastModified:   koboTime(time.Now()),
						},
						BookMetadata: BookMetadata{
							EntitlementId: uuid, CrossRevisionId: uuid, RevisionId: uuid,
							Categories:   []string{"00000000-0000-0000-0000-000000000001"},
							DownloadUrls: []DownloadURL{}, ExternalIds: []string{},
						},
					},
				})
			}
			if err := h.engine.forgetSynced(ctx, u.ID, removed); err != nil {
				h.log.Error("kobo sync: forget removed", "err", err)
			}
		}
	}

	// --- shelves as collections --------------------------------------------
	shelves, err := h.engine.changedShelves(ctx, u.ID, tok.TagsLastModified)
	if err != nil {
		h.log.Error("kobo sync: shelves", "err", err)
	}
	newestTag := tok.TagsLastModified
	for _, s := range shelves {
		tag := Tag{
			Id: s.UUID, Name: s.Name, Type: "UserTag",
			Created:      koboTime(s.Created),
			LastModified: koboTime(s.Modified),
		}
		for _, bu := range s.Items {
			tag.Items = append(tag.Items, TagItem{RevisionId: bu, Type: "ProductRevisionTagItem"})
		}
		if s.IsNew {
			items = append(items, map[string]any{"NewTag": NewTag{Tag: tag}})
		} else {
			items = append(items, map[string]any{"ChangedTag": ChangedTag{Tag: tag}})
		}
		if s.Modified.After(newestTag) {
			newestTag = s.Modified
		}
	}
	if deleted, err := h.engine.deletedShelves(ctx, u.ID, tok.TagsLastModified); err == nil {
		for _, uuid := range deleted {
			var d DeletedTag
			d.Tag.Id = uuid
			items = append(items, map[string]any{"DeletedTag": d})
		}
	}

	// --- reading state ------------------------------------------------------
	states, moreStates, err := h.engine.changedReadingStates(ctx, u.ID,
		tok.ReadingStateLastModified, h.syncLimit)
	if err != nil {
		h.log.Error("kobo sync: reading states", "err", err)
	}
	contSync = contSync || moreStates
	newestState := tok.ReadingStateLastModified
	for i := range states {
		rs := readingStateJSON(&states[i])
		items = append(items, map[string]any{"ChangedReadingState": ChangedReadingState{ReadingState: rs}})
		if states[i].Modified.After(newestState) {
			newestState = states[i].Modified
		}
	}

	if err := h.engine.markSynced(ctx, u.ID, syncedIDs); err != nil {
		h.log.Error("kobo sync: mark synced", "err", err)
	}

	// Advance the watermarks. When more work remains, books_last_created is
	// deliberately held back so the next request continues rather than
	// treating the rest as already delivered.
	tok.BooksLastModified = newest
	if !contSync {
		tok.BooksLastCreated = newest
		tok.ArchiveLastModified = time.Now().UTC()
	}
	tok.TagsLastModified = newestTag
	tok.ReadingStateLastModified = newestState

	// Fold in whatever the real Kobo store had to say, if proxying is on.
	// Without this, a device with purchased Kobo books would watch them vanish
	// the moment its library moved here.
	var storeItems int
	if store := <-storeCh; store != nil {
		storeItems = len(store.Items)
		var storeCont bool
		// Budget the store the same page limit our own entities get, so one
		// response can never exceed what the device is built to read.
		items, storeCont = store.apply(items, tok, w.Header(), h.syncLimit*2)
		contSync = contSync || storeCont
	}

	// Encode into a buffer rather than streaming to the socket.
	//
	// Go switches to chunked transfer encoding once a response passes its 2 KB
	// sniff buffer, and a sync response is normally larger than that, so the
	// device was getting a chunked body with no Content-Length while
	// calibre-web (Flask) always sends one. Sync responses are bounded by the
	// page limit, so buffering costs a few hundred KB at most and removes a
	// difference from the one client we cannot debug.
	body, err := json.Marshal(items)
	if err != nil {
		h.log.Error("kobo sync: encode", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	body = append(body, '\n')

	tok.WriteHeader(w.Header())
	if contSync {
		setKoboHeader(w.Header(), "x-kobo-sync", "continue")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if _, err := w.Write(body); err != nil {
		h.log.Error("kobo sync: write", "err", err)
	}

	h.log.Info("kobo sync", "user", u.Username, "items", len(items),
		"books", len(books), "shelves", len(shelves), "states", len(states),
		"from_kobo_store", storeItems, "continue", contSync)
}

// ensureKepub queues a conversion if the book has an EPUB but no KEPUB yet.
func (h *Handler) ensureKepub(ctx context.Context, b *syncBook) {
	var epubName string
	err := h.pool.QueryRow(ctx, `
		SELECT filename FROM book_files WHERE book_id=$1 AND format='EPUB'`, b.ID).Scan(&epubName)
	if err != nil {
		return
	}
	src := filepath.Join(h.libraryRoot, b.Path, epubName)
	if _, ok := h.kepub.Cached(b.UUID, src); ok {
		return
	}
	// Highest priority: a book on a Kobo shelf is wanted now, ahead of any
	// library-wide backfill.
	if err := h.queue.Enqueue(ctx, jobs.KindKepub, b.UUID,
		kepub.Payload{BookID: b.ID, UUID: b.UUID, SrcPath: src}, 10); err != nil {
		h.log.Warn("could not queue kepub conversion", "book", b.ID, "err", err)
	}
}

// metadataFor builds the BookMetadata for a book.
func (h *Handler) metadataFor(r *http.Request, b *syncBook) BookMetadata {
	tok := tokenOf(r)
	md := BookMetadata{
		Categories:      []string{"00000000-0000-0000-0000-000000000001"},
		CoverImageId:    b.UUID,
		CrossRevisionId: b.UUID,
		EntitlementId:   b.UUID,
		ExternalIds:     []string{},
		Genre:           "00000000-0000-0000-0000-000000000001",
		IsSocialEnabled: true,
		Language:        threeLetterLang(b.Language),
		RevisionId:      b.UUID,
		Title:           b.Title,
		WorkId:          b.UUID,
		DownloadUrls:    []DownloadURL{},
	}
	md.CurrentDisplayPrice.CurrencyCode = "USD"

	for _, a := range b.Authors {
		md.Contributors = append(md.Contributors, a)
		md.ContributorRoles = append(md.ContributorRoles, ContributorRole{Name: a})
	}
	if b.Description != nil {
		md.Description = *b.Description
	}
	if b.Publisher != nil {
		md.Publisher = &struct {
			Imprint string `json:"Imprint"`
			Name    string `json:"Name"`
		}{Name: *b.Publisher}
	}
	if b.PubDate != nil {
		md.PublicationDate = koboDate(*b.PubDate)
	}
	if b.Series != nil && *b.Series != "" {
		si := &SeriesInfo{Name: *b.Series, Id: *b.Series}
		if b.SeriesIndex != nil {
			si.NumberFloat = *b.SeriesIndex
			si.Number = int(*b.SeriesIndex)
		}
		md.Series = si
	}

	// Advertise KEPUB only once the converted file actually exists. Offering a
	// format we would have to build on demand is exactly what makes
	// calibre-web's sync time out.
	for _, f := range h.formatsFor(r.Context(), b) {
		md.DownloadUrls = append(md.DownloadUrls, DownloadURL{
			Format:   f.format,
			Size:     f.size,
			Url:      downloadURL(h.externalURL, tok, b.ID, f.format),
			Platform: "Generic",
			DrmType:  "None",
		})
	}
	return md
}

type fmtInfo struct {
	format string
	size   int64
}

// formatsFor lists what the device may download, preferring KEPUB.
func (h *Handler) formatsFor(ctx context.Context, b *syncBook) []fmtInfo {
	rows, err := h.pool.Query(ctx,
		`SELECT format, filename, size_bytes FROM book_files WHERE book_id=$1`, b.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var epub *fmtInfo
	var kepubOnDisk *fmtInfo
	var epubPath string
	for rows.Next() {
		var f, name string
		var size int64
		if err := rows.Scan(&f, &name, &size); err != nil {
			return nil
		}
		switch strings.ToUpper(f) {
		case "EPUB":
			epub = &fmtInfo{format: "EPUB", size: size}
			epubPath = filepath.Join(h.libraryRoot, b.Path, name)
		case "KEPUB":
			kepubOnDisk = &fmtInfo{format: "KEPUB", size: size}
		}
	}

	// Calibre already produced a KEPUB for most of this library; prefer it.
	if kepubOnDisk != nil {
		return []fmtInfo{*kepubOnDisk}
	}
	// Otherwise use our converted copy, but only if it is ready.
	if epub != nil {
		if p, ok := h.kepub.Cached(b.UUID, epubPath); ok {
			if st, err := os.Stat(p); err == nil {
				return []fmtInfo{{format: "KEPUB", size: st.Size()}}
			}
		}
		return []fmtInfo{*epub}
	}
	return nil
}

// entitlementFor builds the BookEntitlement for a book.
func (h *Handler) entitlementFor(b *syncBook) BookEntitlement {
	e := BookEntitlement{
		Accessibility:       "Full",
		Created:             koboTime(b.Created),
		CrossRevisionId:     b.UUID,
		Id:                  b.UUID,
		IsRemoved:           false,
		IsHiddenFromArchive: false,
		IsLocked:            false,
		LastModified:        koboTime(b.Changed),
		OriginCategory:      "Imported",
		RevisionId:          b.UUID,
		Status:              "Active",
	}
	e.ActivePeriod.From = koboTime(b.Created)
	return e
}

// readingStateJSON converts a stored row to the device's shape.
func readingStateJSON(s *readingStateRow) ReadingState {
	rs := ReadingState{
		Created:           koboTime(s.Priority),
		EntitlementId:     s.BookUUID,
		LastModified:      koboTime(s.Modified),
		PriorityTimestamp: koboTime(s.Priority),
		StatusInfo: StatusInfo{
			LastModified:        koboTime(s.Modified),
			Status:              s.Status,
			TimesStartedReading: s.TimesStarted,
		},
		CurrentBookmark: Bookmark{LastModified: koboTime(s.Modified)},
		Statistics:      Statistics{LastModified: koboTime(s.Modified)},
	}
	if s.LastStarted != nil {
		rs.StatusInfo.LastTimeStartedReading = koboTime(*s.LastStarted)
	}
	if s.LastFinished != nil {
		rs.StatusInfo.LastTimeFinished = koboTime(*s.LastFinished)
	}
	if s.Progress != nil {
		rs.CurrentBookmark.ProgressPercent = *s.Progress
	}
	if s.SrcProgress != nil {
		rs.CurrentBookmark.ContentSourceProgressPercent = *s.SrcProgress
	}
	if s.LocValue != nil {
		rs.CurrentBookmark.Location.Value = *s.LocValue
	}
	if s.LocType != nil {
		rs.CurrentBookmark.Location.Type = *s.LocType
	}
	if s.LocSource != nil {
		rs.CurrentBookmark.Location.Source = *s.LocSource
	}
	if s.Spent != nil {
		rs.Statistics.SpentReadingMinutes = *s.Spent
	}
	if s.Remaining != nil {
		rs.Statistics.RemainingTimeMinutes = *s.Remaining
	}
	return rs
}

// threeLetterLang maps our stored code to what the device expects.
func threeLetterLang(code string) string {
	if code == "" {
		return "en"
	}
	return code
}

var errNotFound = errors.New("not found")

// bookByUUID resolves a device-supplied uuid to a book id and path.
func (h *Handler) bookByUUID(ctx context.Context, uuid string) (int64, string, error) {
	var id int64
	var path string
	err := h.pool.QueryRow(ctx, `SELECT id, path FROM books WHERE uuid = $1`, uuid).Scan(&id, &path)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", errNotFound
	}
	return id, path, err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func parseIntDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

var _ = fmt.Sprintf
