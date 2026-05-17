package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// --- Password helpers --------------------------------------------------------

func hashPassword(plain string) (string, error) {
	if len(plain) < 6 {
		return "", errors.New("password must be at least 6 characters")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func verifyPassword(hash, plain string) bool {
	if hash == "" || plain == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// --- Allowed-origin endpoints ------------------------------------------------

func (s *Server) handleOrigins(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListAllowedOrigins(r.Context())
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var body struct {
			Origin string `json:"origin"`
			Label  string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			badRequest(w, err)
			return
		}
		body.Origin = strings.TrimSpace(body.Origin)
		if body.Origin == "" {
			badRequest(w, errors.New("origin is required"))
			return
		}
		o, err := s.store.CreateAllowedOrigin(r.Context(), "", body.Origin, body.Label)
		if err != nil {
			serverError(w, err)
			return
		}
		s.origins.invalidate()
		writeJSON(w, http.StatusCreated, o)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleOriginByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/admin/origins/")
	if id == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var body struct {
			Label   string `json:"label"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			badRequest(w, err)
			return
		}
		o, err := s.store.UpdateAllowedOrigin(r.Context(), id, body.Label, body.Enabled)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		s.origins.invalidate()
		writeJSON(w, http.StatusOK, o)
	case http.MethodDelete:
		if err := s.store.DeleteAllowedOrigin(r.Context(), id); err != nil {
			handleStoreError(w, err)
			return
		}
		s.origins.invalidate()
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

// --- User endpoints ----------------------------------------------------------

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListUsers(r.Context())
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			badRequest(w, err)
			return
		}
		body.Username = strings.TrimSpace(body.Username)
		if body.Username == "" {
			badRequest(w, errors.New("username is required"))
			return
		}
		if body.Role == "" {
			body.Role = "admin"
		}
		if body.Role != "admin" && body.Role != "viewer" {
			badRequest(w, errors.New("role must be admin or viewer"))
			return
		}
		hash, err := hashPassword(body.Password)
		if err != nil {
			badRequest(w, err)
			return
		}
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		u, err := s.store.CreateUser(r.Context(), body.Username, hash, body.Role, enabled)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, u)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleUserByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/users/")
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		s.handleUserRecord(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "password" {
		s.handleUserPassword(w, r, id)
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

func (s *Server) handleUserRecord(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		u, err := s.store.GetUser(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, u)
	case http.MethodPatch:
		var body struct {
			Role    string `json:"role"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			badRequest(w, err)
			return
		}
		if body.Role != "" && body.Role != "admin" && body.Role != "viewer" {
			badRequest(w, errors.New("role must be admin or viewer"))
			return
		}
		u, err := s.store.UpdateUser(r.Context(), id, body.Role, body.Enabled)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		if !body.Enabled {
			_ = s.store.DeleteUserSessions(r.Context(), id)
		}
		writeJSON(w, http.StatusOK, u)
	case http.MethodDelete:
		sess, _ := s.currentSession(r)
		if sess.UserID == id {
			writeError(w, http.StatusBadRequest, "cannot delete your own account")
			return
		}
		if err := s.store.DeleteUser(r.Context(), id); err != nil {
			handleStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, err)
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		badRequest(w, err)
		return
	}
	if err := s.store.SetUserPassword(r.Context(), id, hash); err != nil {
		handleStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// EnsureSeedData seeds the first admin user and the initial allowed-origin
// list from environment variables if both tables are empty. Safe to call on
// every boot.
func (s *Server) EnsureSeedData() error {
	ctx := s.rootCtx

	count, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		username := s.cfg.AdminUsername
		if username == "" {
			username = "admin"
		}
		password := s.cfg.AdminPassword
		if password == "" {
			password = s.cfg.AdminToken
		}
		if password == "" {
			password = "change-me-now"
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		if _, err := s.store.CreateUser(ctx, username, hash, "admin", true); err != nil {
			return err
		}
		s.logger.Info("seeded initial admin user", "username", username)
	}
	return nil
}
