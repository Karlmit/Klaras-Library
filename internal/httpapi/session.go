package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Karlmit/Klaras-Library/internal/auth"
)

type ctxKey int

const userCtxKey ctxKey = iota

const sessionUserKey = "user_id"

// currentUser returns the authenticated user, or nil.
func (s *Server) currentUser(r *http.Request) *auth.User {
	u, _ := r.Context().Value(userCtxKey).(*auth.User)
	return u
}

// loadUser rehydrates the session user on every request. It never rejects:
// authorisation is the job of requireRole, so public endpoints stay public.
func (s *Server) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.sessions.GetInt64(r.Context(), sessionUserKey)
		if id > 0 {
			// Loading from the database on each request rather than trusting a
			// cached copy in the session means a role change or a disabled
			// account takes effect immediately, not at next login.
			if u, err := s.auth.ByID(r.Context(), id); err == nil {
				ctx := context.WithValue(r.Context(), userCtxKey, u)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// Stale or disabled: drop the session rather than loop on errors.
			_ = s.sessions.Destroy(r.Context())
		}
		next.ServeHTTP(w, r)
	})
}

// requireRole rejects requests from users below the given role.
func (s *Server) requireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := s.currentUser(r)
			if u == nil {
				writeErr(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if !u.Can(role) {
				writeErr(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			// An imported user has no usable password yet. Let them set one,
			// but nothing else, so a half-migrated account cannot be used.
			if u.PasswordResetRequired && !isPasswordEndpoint(r) {
				writeErr(w, http.StatusForbidden, "password reset required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isPasswordEndpoint(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/auth/password")
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	u, err := s.auth.Authenticate(r.Context(), in.Username, in.Password)
	if err != nil {
		s.log.Warn("failed login", "username", in.Username, "ip", r.RemoteAddr)
		// One message for both wrong-user and wrong-password, so the response
		// does not confirm which usernames exist.
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	// A new session id on login defeats session fixation.
	if err := s.sessions.RenewToken(r.Context()); err != nil {
		s.fail(w, r, err, "renew session")
		return
	}
	s.sessions.Put(r.Context(), sessionUserKey, u.ID)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Destroy(r.Context()); err != nil {
		s.fail(w, r, err, "destroy session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user": u})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	// An imported user has no working password to prove, so the current-password
	// check is skipped exactly once, for them.
	if !u.PasswordResetRequired {
		if _, err := s.auth.Authenticate(r.Context(), u.Username, in.Current); err != nil {
			writeErr(w, http.StatusForbidden, "current password is incorrect")
			return
		}
	}
	if err := s.auth.SetPassword(r.Context(), u.ID, in.New); err != nil {
		if err == auth.ErrPasswordTooShort {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.fail(w, r, err, "set password")
		return
	}
	// Changing a password invalidates other sessions.
	if err := s.sessions.RenewToken(r.Context()); err != nil {
		s.fail(w, r, err, "renew session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password updated"})
}

// handleSetup creates the first admin account. Available only while no usable
// admin exists, so it cannot be used to add one later.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	needs, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		s.fail(w, r, err, "setup check")
		return
	}
	if !needs {
		writeErr(w, http.StatusConflict, "setup has already been completed")
		return
	}
	var in struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if strings.TrimSpace(in.Username) == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}

	u, err := s.auth.CreateUser(r.Context(), in.Username, in.Email, in.Password, auth.RoleAdmin)
	if err != nil {
		if err == auth.ErrPasswordTooShort {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Contains(err.Error(), "duplicate") {
			writeErr(w, http.StatusConflict, "that username is already taken")
			return
		}
		s.fail(w, r, err, "create first admin")
		return
	}
	s.log.Info("first admin created", "username", u.Username)
	_ = s.sessions.RenewToken(r.Context())
	s.sessions.Put(r.Context(), sessionUserKey, u.ID)
	writeJSON(w, http.StatusCreated, u)
}

// handleStatus reports whether the server still needs first-run setup.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		s.fail(w, r, err, "status")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": needs,
		"version":     s.version,
	})
}
