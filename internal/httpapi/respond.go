package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// apiError is the single error shape every endpoint returns, so the client has
// one thing to parse.
type apiError struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

// fail maps an internal error to a response, logging the detail but never
// leaking it to the client.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error, msg string) {
	if errors.Is(err, library.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, r.Context().Err()) && r.Context().Err() != nil {
		// The client went away; nothing useful to send.
		return
	}
	s.log.Error(msg, "err", err, "path", r.URL.Path)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

// intParam reads a positive integer path parameter.
func intParam(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, name), 10, 64)
}

// queryInt reads an integer query parameter with a default.
func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// queryBool reads a boolean query parameter.
func queryBool(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "1" || v == "true" || v == "yes"
}

var _ = slog.Default
