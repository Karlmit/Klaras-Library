// Package httpapi wires the HTTP router, middleware and handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Karlmit/Klaras-Library/internal/auth"
	"github.com/Karlmit/Klaras-Library/internal/config"
	"github.com/Karlmit/Klaras-Library/internal/covers"
	"github.com/Karlmit/Klaras-Library/internal/filestore"
	"github.com/Karlmit/Klaras-Library/internal/ingest"
	"github.com/Karlmit/Klaras-Library/internal/jobs"
	"github.com/Karlmit/Klaras-Library/internal/kepub"
	"github.com/Karlmit/Klaras-Library/internal/kobo"
	"github.com/Karlmit/Klaras-Library/internal/library"
	"github.com/Karlmit/Klaras-Library/internal/opds"
	"github.com/Karlmit/Klaras-Library/internal/provider"
	"github.com/Karlmit/Klaras-Library/internal/store"
	"github.com/Karlmit/Klaras-Library/web"
)

// Server holds everything the HTTP handlers need.
type Server struct {
	cfg       *config.Config
	db        *store.DB
	lib       *library.Store
	auth      *auth.Service
	covers    *covers.Service
	kepub     *kepub.Service
	files     *filestore.Store
	ingest    *ingest.Service
	providers *provider.Set
	limiter   *auth.Limiter
	queue     *jobs.Queue
	sessions  *scs.SessionManager
	log       *slog.Logger
	version   string
	router    chi.Router
	started   time.Time
}

// Deps are the collaborators a Server needs.
type Deps struct {
	Config    *config.Config
	DB        *store.DB
	Library   *library.Store
	Auth      *auth.Service
	Covers    *covers.Service
	Kepub     *kepub.Service
	Files     *filestore.Store
	Ingest    *ingest.Service
	Providers *provider.Set
	Queue     *jobs.Queue
	Log       *slog.Logger
	Version   string
}

// New builds the server and its routes.
func New(d Deps) *Server {
	sm := scs.New()
	sm.Store = pgxstore.New(d.DB.Pool)
	sm.Lifetime = auth.SessionLifetime
	sm.Cookie.Name = "klaras_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Path = "/"
	// Secure cookies require https. Setting it unconditionally would break a
	// plain-http LAN setup by making the cookie unusable, so it follows the
	// externally advertised scheme.
	sm.Cookie.Secure = d.Config.ExternalURL == "" ||
		len(d.Config.ExternalURL) >= 5 && d.Config.ExternalURL[:5] == "https"

	s := &Server{
		// 8 failures in 15 minutes, then locked out for 15. Loose enough that
		// mistyping a passphrase never bites, tight enough that online guessing
		// against an argon2id hash is hopeless.
		limiter: auth.NewLimiter(8, 15*time.Minute, 15*time.Minute),

		cfg: d.Config, db: d.DB, lib: d.Library, auth: d.Auth,
		covers: d.Covers, kepub: d.Kepub, files: d.Files, ingest: d.Ingest,
		providers: d.Providers, queue: d.Queue, sessions: sm,
		log: d.Log, version: d.Version, started: time.Now(),
	}
	s.routes()
	return s
}

// Limiter exposes the auth failure limiter so the caller can run its sweeper.
func (s *Server) Limiter() *auth.Limiter { return s.limiter }

// Sessions exposes the session manager so the caller can run its cleanup.
func (s *Server) Sessions() *scs.SessionManager { return s.sessions }

func (s *Server) routes() {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(s.securityHeaders)

	// Health endpoints sit outside the session manager: they must answer even
	// if the session store is unhappy.
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)

	// Kobo endpoints sit outside the session middleware entirely: the device
	// authenticates with a token in the URL path and has no cookie jar.
	kobo.NewHandler(kobo.Deps{
		Pool: s.db.Pool, Auth: s.auth, Kepub: s.kepub, Covers: s.covers,
		Queue: s.queue, Limiter: s.limiter, LibraryRoot: s.cfg.LibraryRoot,
		ExternalURL: s.cfg.ExternalURL, ProxyStore: s.cfg.KoboProxyStore,
		SyncLimit: s.cfg.KoboSyncLimit, Log: s.log,
	}).Routes(r)

	// OPDS also sits outside the session: readers authenticate with HTTP Basic.
	opds.New(s.lib, s.auth, s.limiter, s.cfg.ExternalURL).Routes(r)

	r.Group(func(r chi.Router) {
		r.Use(s.sessions.LoadAndSave)
		r.Use(s.loadUser)
		r.Use(middleware.Compress(5))

		r.Route("/api", func(r chi.Router) {
			// Public.
			r.Get("/status", s.handleStatus)
			r.Post("/setup", s.handleSetup)
			r.Post("/auth/login", s.handleLogin)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/password", s.handleChangePassword)

			// Reading the catalogue.
			r.Group(func(r chi.Router) {
				r.Use(s.requireRole(auth.RoleReader))
				r.Get("/books", s.handleListBooks)
				r.Get("/books/{id}", s.handleGetBook)
				r.Get("/books/{id}/cover", s.handleCover)
				r.Get("/books/{id}/cover/{size}", s.handleCover)
				r.Get("/facets", s.handleFacets)
				r.Get("/suggest", s.handleSuggest)
				r.Get("/shelves", s.handleListShelves)
				r.Post("/shelves", s.handleCreateShelf)
				r.Patch("/shelves/{id}", s.handleUpdateShelf)
				r.Delete("/shelves/{id}", s.handleDeleteShelf)
				r.Post("/shelves/{id}/books", s.handleShelfBooks)
				r.Post("/shelves/{id}/kobo-subscription", s.handleKoboSubscribe)
				r.Delete("/shelves/{id}/kobo-subscription", s.handleKoboSubscribe)
				r.Get("/books/ids", s.handleBookIDs)
				r.Get("/kobo/tokens", s.handleListKoboTokens)
				r.Post("/kobo/tokens", s.handleKoboToken)
				r.Post("/kobo/resync", s.handleKoboResync)
				r.Get("/books/{id}/progress", s.handleGetProgress)
				r.Put("/books/{id}/progress", s.handlePutProgress)
				r.Get("/books/{id}/download/{format}", s.handleDownloadBook)
			})

			// Changing the library needs the editor role.
			r.Group(func(r chi.Router) {
				r.Use(s.requireRole(auth.RoleEditor))
				r.Patch("/books/{id}", s.handleUpdateBook)
				r.Post("/books/bulk", s.handleBulkUpdate)
				r.Get("/metadata/search", s.handleMetadataSearch)
				r.Delete("/books/{id}", s.handleDeleteBook)
				r.Post("/books/bulk-delete", s.handleBulkDelete)
				r.Post("/books/upload", s.handleUploadBook)
				r.Put("/books/{id}/cover", s.handleReplaceCover)
			})

			// Account administration.
			r.Group(func(r chi.Router) {
				r.Use(s.requireRole(auth.RoleAdmin))
				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Patch("/users/{id}", s.handleUpdateUser)
				r.Put("/users/{id}/password", s.handleSetUserPassword)
			})
		})
	})

	// The SPA is mounted last so it only catches paths no API route claimed.
	if assets, err := web.Assets(); err != nil {
		s.log.Error("embedded UI unavailable", "err", err)
	} else {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Compress(5))
			s.mountSPA(r, assets)
		})
	}

	s.router = r
}

// ServeHTTP makes Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// securityHeaders sets the headers that cost nothing and prevent whole classes
// of problem. The CSP is deliberately strict: this app loads no third-party
// script, style, font or image, so nothing legitimate is blocked.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		// blob: is allowed for framing, images, fonts and media, but deliberately
		// NOT for script-src. The in-browser reader hands epub.js the book as a
		// blob and it renders into a blob: iframe; without frame-src the reader
		// silently hangs on "Opening...". Book content can therefore be
		// displayed but never executed, and epub.js additionally sandboxes the
		// iframe against scripting by default.
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"img-src 'self' data: blob:; "+
				"style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; "+
				"font-src 'self' data: blob:; "+
				"media-src 'self' blob:; "+
				"connect-src 'self' blob:; "+
				"frame-src 'self' blob:; "+
				"child-src 'self' blob:; "+
				"worker-src 'self' blob:; "+
				"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.version,
		"uptime":  time.Since(s.started).Round(time.Second).String(),
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.db.Health(ctx); err != nil {
		s.log.Warn("readiness check failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable", "error": "database unreachable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

// requestLogger logs one line per request, escalating by status.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		defer func() {
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", float64(time.Since(start).Microseconds()) / 1000,
				"req_id", middleware.GetReqID(r.Context()),
			}
			switch {
			case ww.Status() >= 500:
				s.log.Error("request", attrs...)
			case ww.Status() >= 400:
				s.log.Warn("request", attrs...)
			default:
				s.log.Debug("request", attrs...)
			}
		}()
		next.ServeHTTP(ww, r)
	})
}
