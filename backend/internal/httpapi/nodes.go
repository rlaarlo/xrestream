package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"restream/backend/internal/store"
)

// --- /me/nodes ---------------------------------------------------------------

// generateNodeAPIKey returns a (plain, hash) pair. Plain is shown once to
// the user; only the bcrypt hash is persisted. The plain key format embeds
// the node ID so node auth can do a single lookup-then-verify.
func generateNodeAPIKey() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(buf)
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", "", err
	}
	return secret, string(hash), nil
}

func (s *Server) handleMyNodes(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		ownerID := sess.UserID
		if sess.Role == "admin" {
			// admin can list all by passing ?all=1
			if r.URL.Query().Get("all") == "1" {
				ownerID = ""
			}
		}
		list, err := s.store.ListNodes(r.Context(), ownerID)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		if sess.UserID == "" {
			writeError(w, http.StatusForbidden, "service token cannot own nodes")
			return
		}
		var body struct {
			Name string `json:"name"`
			Host string `json:"host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			badRequest(w, err)
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			badRequest(w, errors.New("name is required"))
			return
		}
		plain, hash, err := generateNodeAPIKey()
		if err != nil {
			serverError(w, err)
			return
		}
		n, err := s.store.CreateNode(r.Context(), sess.UserID, body.Name, body.Host, hash)
		if err != nil {
			serverError(w, err)
			return
		}
		// API key shown ONCE — combine node ID + secret so node can present
		// "<id>.<secret>" to /node/* endpoints.
		writeJSON(w, http.StatusCreated, map[string]any{
			"node":   n,
			"apiKey": n.ID + "." + plain,
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleMyNodeByID(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/me/nodes/")
	if id == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	n, err := s.store.GetNode(r.Context(), id)
	if err != nil {
		handleStoreError(w, err)
		return
	}
	if sess.Role != "admin" && n.OwnerID != sess.UserID {
		writeError(w, http.StatusForbidden, "not your node")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, n)
	case http.MethodPatch:
		var body struct {
			Name string `json:"name"`
			Host string `json:"host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			badRequest(w, err)
			return
		}
		updated, err := s.store.UpdateNode(r.Context(), id, body.Name, body.Host)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := s.store.DeleteNode(r.Context(), id); err != nil {
			handleStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

// --- /me/r2 ------------------------------------------------------------------

func (s *Server) handleMyR2(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if sess.UserID == "" {
		writeError(w, http.StatusForbidden, "service token has no R2 config")
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.store.GetR2Config(r.Context(), sess.UserID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSON(w, http.StatusOK, nil)
				return
			}
			serverError(w, err)
			return
		}
		// Mask secret in API response.
		cfg.SecretAccessKey = maskSecret(cfg.SecretAccessKey)
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodPut:
		var body struct {
			AccountID       string `json:"accountId"`
			AccessKeyID     string `json:"accessKeyId"`
			SecretAccessKey string `json:"secretAccessKey"`
			Bucket          string `json:"bucket"`
			PublicURL       string `json:"publicUrl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			badRequest(w, err)
			return
		}
		if strings.TrimSpace(body.AccountID) == "" || strings.TrimSpace(body.AccessKeyID) == "" || strings.TrimSpace(body.Bucket) == "" {
			badRequest(w, errors.New("accountId, accessKeyId, and bucket are required"))
			return
		}
		// If client did not send a fresh secret, preserve the existing one.
		secret := body.SecretAccessKey
		if secret == "" {
			if existing, err := s.store.GetR2Config(r.Context(), sess.UserID); err == nil {
				secret = existing.SecretAccessKey
			}
		}
		if strings.TrimSpace(secret) == "" {
			badRequest(w, errors.New("secretAccessKey is required"))
			return
		}
		cfg, err := s.store.UpsertR2Config(r.Context(), sess.UserID, body.AccountID, body.AccessKeyID, secret, body.Bucket, body.PublicURL)
		if err != nil {
			serverError(w, err)
			return
		}
		cfg.SecretAccessKey = maskSecret(cfg.SecretAccessKey)
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodDelete:
		if err := s.store.DeleteR2Config(r.Context(), sess.UserID); err != nil && !errors.Is(err, store.ErrNotFound) {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}

// --- /me/origins -------------------------------------------------------------

func (s *Server) handleMyOrigins(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if sess.UserID == "" {
		writeError(w, http.StatusForbidden, "service token cannot own origins")
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListAllowedOriginsForOwner(r.Context(), sess.UserID)
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
		if strings.TrimSpace(body.Origin) == "" {
			badRequest(w, errors.New("origin is required"))
			return
		}
		o, err := s.store.CreateAllowedOrigin(r.Context(), sess.UserID, body.Origin, body.Label)
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

func (s *Server) handleMyOriginByID(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if sess.UserID == "" {
		writeError(w, http.StatusForbidden, "service token cannot own origins")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/me/origins/")
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
		o, err := s.store.UpdateAllowedOriginForOwner(r.Context(), sess.UserID, id, body.Label, body.Enabled)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		s.origins.invalidate()
		writeJSON(w, http.StatusOK, o)
	case http.MethodDelete:
		if err := s.store.DeleteAllowedOriginForOwner(r.Context(), sess.UserID, id); err != nil {
			handleStoreError(w, err)
			return
		}
		s.origins.invalidate()
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

// --- /node/* (node-side authentication) --------------------------------------

// authenticateNode verifies "<node_id>.<secret>" from Authorization or
// X-Node-Key and returns the node.
func (s *Server) authenticateNode(r *http.Request) (store.Node, bool) {
	key := r.Header.Get("X-Node-Key")
	if key == "" {
		key = bearerToken(r)
	}
	if key == "" {
		return store.Node{}, false
	}
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return store.Node{}, false
	}
	n, hash, err := s.store.GetNodeWithKeyHash(r.Context(), parts[0])
	if err != nil {
		return store.Node{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(parts[1])) != nil {
		return store.Node{}, false
	}
	return n, true
}

func (s *Server) handleNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	n, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid node key")
		return
	}
	if err := s.store.TouchNodeHeartbeat(r.Context(), n.ID); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "nodeId": n.ID})
}

func (s *Server) handleNodeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	n, ok := s.authenticateNode(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid node key")
		return
	}
	resp := map[string]any{
		"node": map[string]any{
			"id":   n.ID,
			"name": n.Name,
			"host": n.Host,
		},
	}
	if cfg, err := s.store.GetR2Config(r.Context(), n.OwnerID); err == nil {
		resp["r2"] = cfg
	}
	channels, err := s.store.ListChannelsForNode(r.Context(), n.ID)
	if err == nil {
		resp["channels"] = channels
	}
	writeJSON(w, http.StatusOK, resp)
}
