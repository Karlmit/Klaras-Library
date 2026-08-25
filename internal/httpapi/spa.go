package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
)

// mountSPA serves the embedded single-page application.
//
// Two distinct caching rules, because the assets have two distinct lifetimes:
// Vite fingerprints hashed filenames, so those are immutable forever, while
// index.html must never be cached or a deploy leaves browsers pinned to a
// stale bundle that references deleted assets.
func (s *Server) mountSPA(r chi.Router, assets fs.FS) {
	fileServer := http.FileServer(http.FS(assets))

	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upath := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
		if upath == "" {
			upath = "index.html"
		}

		f, err := assets.Open(upath)
		if err != nil {
			// Unknown path with no file extension: a client-side route, so hand
			// back index.html and let the SPA router deal with it. Anything
			// that looks like a missing asset gets an honest 404 instead.
			if path.Ext(upath) != "" {
				http.NotFound(w, req)
				return
			}
			s.serveIndex(w, req, assets)
			return
		}
		_ = f.Close()

		if strings.HasPrefix(upath, "assets/") {
			// Fingerprinted by the bundler; the name changes when the content does.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if upath == "index.html" {
			s.serveIndex(w, req, assets)
			return
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fileServer.ServeHTTP(w, req)
	}))
}

func (s *Server) serveIndex(w http.ResponseWriter, req *http.Request, assets fs.FS) {
	b, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "UI not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
