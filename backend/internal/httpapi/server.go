package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"restream/backend/internal/config"
	"restream/backend/internal/relay"
	"restream/backend/internal/store"
)

type Server struct {
	cfg     config.Config
	store   *store.Store
	relay   *relay.Manager
	logger  *slog.Logger
	router  *http.ServeMux
	rootCtx context.Context
	handler http.Handler

	auth    *authCache
	origins *originCache
}

func NewServer(cfg config.Config, store *store.Store, relayManager *relay.Manager, logger *slog.Logger) *Server {
	server := &Server{
		cfg:     cfg,
		store:   store,
		relay:   relayManager,
		logger:  logger,
		router:  http.NewServeMux(),
		rootCtx: context.Background(),
		auth:    newAuthCache(),
		origins: newOriginCache(),
	}
	server.routes()
	server.handler = server.withCORS(server.router)
	return server
}

// ServeHTTP makes *Server an http.Handler so it can be passed directly to
// http.Server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.router.HandleFunc("/health", s.health)
	s.router.HandleFunc("/auth/login", s.login)
	s.router.HandleFunc("/auth/signup", s.signup)
	s.router.HandleFunc("/auth/config", s.authConfig)
	s.router.HandleFunc("/auth/logout", s.logout)
	s.router.HandleFunc("/auth/me", s.authMe)
	s.router.HandleFunc("/channels", s.channels)
	s.router.HandleFunc("/channels/", s.channelByID)
	s.router.HandleFunc("/proxy/", s.proxy)
	s.router.HandleFunc("/share/", s.share)
	s.router.HandleFunc("/embed/", s.embed)
	s.router.HandleFunc("/admin/origins", s.handleOrigins)
	s.router.HandleFunc("/admin/origins/", s.handleOriginByID)
	s.router.HandleFunc("/admin/users", s.handleUsers)
	s.router.HandleFunc("/admin/users/", s.handleUserByID)
	s.router.HandleFunc("/me/nodes", s.handleMyNodes)
	s.router.HandleFunc("/me/nodes/", s.handleMyNodeByID)
	s.router.HandleFunc("/me/r2", s.handleMyR2)
	s.router.HandleFunc("/me/origins", s.handleMyOrigins)
	s.router.HandleFunc("/me/origins/", s.handleMyOriginByID)
	s.router.HandleFunc("/node/heartbeat", s.handleNodeHeartbeat)
	s.router.HandleFunc("/node/config", s.handleNodeConfig)
	s.router.HandleFunc("/agent/", s.handleAgentDownload)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAgentDownload serves the node-agent binary so VPS workers can pull
// it directly from the control plane. Only whitelisted filenames are served.
func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/agent/")
	allowed := map[string]bool{
		"restream-api-linux-amd64": true,
		"restream-api-linux-arm64": true,
	}
	if !allowed[name] {
		writeError(w, http.StatusNotFound, "binary not found")
		return
	}

	// Try the configured dir first, then a small set of sensible
	// fallbacks so a vanilla "drop binaries next to restream-api" deploy
	// works without setting AGENT_BINARY_DIR.
	candidates := []string{filepath.Join(s.cfg.AgentBinaryDir, name)}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "bin", name),
			filepath.Join(dir, "agent", name),
			filepath.Join(dir, name),
		)
	}

	var (
		f       *os.File
		openErr error
		picked  string
	)
	for _, p := range candidates {
		if g, err := os.Open(p); err == nil {
			f = g
			picked = p
			break
		} else {
			openErr = err
		}
	}
	if f == nil {
		s.logger.Warn("agent binary not found", "name", name, "tried", candidates, "lastErr", openErr)
		writeError(w, http.StatusNotFound, "binary not available on this server")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\"restream-api\"")
	s.logger.Info("agent binary served", "name", name, "path", picked, "bytes", info.Size())
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, err)
		return
	}

	// Static service token (env-based) — still supported for scripts/CI.
	if body.Token != "" && s.cfg.AdminToken != "" && body.Token == s.cfg.AdminToken {
		writeJSON(w, http.StatusOK, map[string]any{
			"token":       s.cfg.AdminToken,
			"username":    "service",
			"role":        "admin",
			"authEnabled": true,
		})
		return
	}

	user, hash, err := s.store.GetUserByUsername(r.Context(), body.Username)
	if err != nil || !verifyPassword(hash, body.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if !user.Enabled {
		writeError(w, http.StatusForbidden, "account is awaiting admin approval")
		return
	}

	token := newSessionToken()
	if err := s.store.CreateSession(r.Context(), user.ID, token, time.Now().Add(sessionTTL)); err != nil {
		serverError(w, err)
		return
	}
	s.store.TouchUserLogin(r.Context(), user.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":       token,
		"username":    user.Username,
		"role":        user.Role,
		"expiresAt":   time.Now().Add(sessionTTL).UTC().Format(time.RFC3339),
		"authEnabled": true,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	token := bearerToken(r)
	if token != "" {
		_ = s.store.DeleteSession(r.Context(), token)
		s.auth.drop(token)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"allowSignup": s.cfg.AllowSignup,
	})
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.cfg.AllowSignup {
		writeError(w, http.StatusForbidden, "signup disabled")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, err)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if len(body.Username) < 3 {
		writeError(w, http.StatusBadRequest, "username must be at least 3 characters")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		badRequest(w, err)
		return
	}
	user, err := s.store.CreateUser(r.Context(), body.Username, hash, "viewer", false)
	if err != nil {
		// Likely unique violation
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"username":      user.Username,
		"role":          user.Role,
		"enabled":       user.Enabled,
		"pendingReview": true,
		"message":       "account created; waiting for admin approval",
	})
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":  sess.Username,
		"role":      sess.Role,
		"expiresAt": sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) channels(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		var (
			channels []store.Channel
			err      error
		)
		if sess.Role == "admin" {
			channels, err = s.store.ListChannels(r.Context())
		} else {
			channels, err = s.store.ListChannelsForOwner(r.Context(), sess.UserID)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		for i := range channels {
			s.applyPublicPlaylistURL(&channels[i])
		}
		writeJSON(w, http.StatusOK, channels)
	case http.MethodPost:
		input, ok := s.decodeChannelInput(w, r)
		if !ok {
			return
		}
		// Non-admin must bind to one of their own nodes; admin can pick any
		// (or none, which keeps the channel on the control plane).
		if input.NodeID != nil && *input.NodeID != "" {
			n, err := s.store.GetNode(r.Context(), *input.NodeID)
			if err != nil {
				handleStoreError(w, err)
				return
			}
			if sess.Role != "admin" && n.OwnerID != sess.UserID {
				writeError(w, http.StatusForbidden, "node is not yours")
				return
			}
		} else if sess.Role != "admin" {
			writeError(w, http.StatusBadRequest, "nodeId is required")
			return
		}
		ownerID := sess.UserID
		channel, err := s.store.CreateChannel(r.Context(), input, s.cfg.PublicStreamURL, ownerID)
		if err != nil {
			serverError(w, err)
			return
		}
		if channel.Mode == "ingest" || channel.Mode == "transmux" {
			if channel.Status == "active" {
				s.relay.StartWorker(s.rootCtx, channel)
			}
		}
		s.applyPublicPlaylistURL(&channel)
		writeJSON(w, http.StatusCreated, channel)
	default:
		methodNotAllowed(w)
	}
}

// applyPublicPlaylistURL always rewrites the channel's playlistUrl using the
// currently configured PublicStreamURL. This ensures that records persisted
// with an older base URL (e.g. http://localhost:3000) are still served with
// the up-to-date public URL without requiring a database migration.
func (s *Server) applyPublicPlaylistURL(channel *store.Channel) {
	if channel == nil || channel.Slug == "" {
		return
	}
	base := strings.TrimRight(s.cfg.PublicStreamURL, "/")
	if base == "" {
		return
	}
	url := fmt.Sprintf("%s/proxy/%s/index.m3u8", base, channel.Slug)
	channel.PlaylistURL = &url
}

func (s *Server) channelByID(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/channels/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	id := parts[0]
	// Enforce ownership: non-admin can only touch own channels.
	if sess.Role != "admin" {
		ch, err := s.store.GetChannel(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		if ch.OwnerID == nil || *ch.OwnerID != sess.UserID {
			writeError(w, http.StatusForbidden, "not your channel")
			return
		}
	}
	if len(parts) == 1 {
		s.handleChannelRecord(w, r, id)
		return
	}

	switch strings.Join(parts[1:], "/") {
	case "metrics":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		metrics, err := s.store.Metrics(r.Context(), id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"playlistRequests": metrics.PlaylistRequests,
			"segmentRequests":  metrics.SegmentRequests,
			"upstreamRequests": metrics.UpstreamRequests,
			"cacheHits":        metrics.CacheHits,
			"cacheMisses":      metrics.CacheMisses,
			"bytesSent":        metrics.BytesSent,
			"bytesUpstream":    metrics.BytesUpstream,
			"workerErrors":     metrics.WorkerErrors,
			"liveViewers":      s.relay.LiveViewers(id),
		})
	case "purge-cache":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		channel, err := s.store.GetChannel(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		if err := s.relay.PurgeChannel(channel.Slug); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"purged": true})
	case "worker/start":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		channel, err := s.store.GetChannel(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		s.relay.StartWorker(s.rootCtx, channel)
		writeJSON(w, http.StatusOK, map[string]bool{"running": s.relay.WorkerRunning(id)})
	case "worker/stop":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		channel, err := s.store.GetChannel(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		s.relay.StopWorker(id)
		_ = s.relay.PurgeChannel(channel.Slug)
		_ = s.store.SetWorkerStatus(r.Context(), id, "stopped", nil)
		writeJSON(w, http.StatusOK, map[string]bool{"running": false})
	case "worker/status":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		channel, err := s.store.GetChannel(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"running": s.relay.WorkerRunning(id), "workerStatus": channel.WorkerStatus})
	case "share-link":
		s.handleShareLink(w, r, id)
	case "playback-token":
		s.handlePlaybackToken(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "route not found")
	}
}

func (s *Server) handleChannelRecord(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		channel, err := s.store.GetChannel(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		s.applyPublicPlaylistURL(&channel)
		writeJSON(w, http.StatusOK, channel)
	case http.MethodPatch:
		input, ok := s.decodeChannelInput(w, r)
		if !ok {
			return
		}
		channel, err := s.store.UpdateChannel(r.Context(), id, input, s.cfg.PublicStreamURL)
		if err != nil {
			serverError(w, err)
			return
		}
		s.relay.StopWorker(id)
		if (channel.Mode == "ingest" || channel.Mode == "transmux") && channel.Status == "active" {
			s.relay.StartWorker(s.rootCtx, channel)
		}
		s.applyPublicPlaylistURL(&channel)
		writeJSON(w, http.StatusOK, channel)
	case http.MethodDelete:
		channel, err := s.store.GetChannel(r.Context(), id)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		s.relay.StopWorker(id)
		_ = s.relay.PurgeChannel(channel.Slug)
		if err := s.store.DeleteChannel(r.Context(), id); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) decodeChannelInput(w http.ResponseWriter, r *http.Request) (store.ChannelInput, bool) {
	var input store.ChannelInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		badRequest(w, err)
		return input, false
	}
	if err := validateChannelInput(r.Context(), input); err != nil {
		badRequest(w, err)
		return input, false
	}
	return input, true
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/proxy/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "proxy route not found")
		return
	}
	slug := parts[0]
	if parts[1] == "index.m3u8" {
		s.serveProxyPlaylist(w, r, slug)
		return
	}
	if parts[1] == "asset" {
		s.serveProxyAsset(w, r, slug)
		return
	}
	if parts[1] == "segment" && len(parts) == 3 && parts[2] != "" {
		s.serveTransmuxSegment(w, r, slug, parts[2])
		return
	}
	writeError(w, http.StatusNotFound, "proxy route not found")
}

func (s *Server) serveProxyPlaylist(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		methodNotAllowed(w)
		return
	}
	exp, sig, ok := s.requirePlaybackToken(w, r, slug)
	if !ok {
		return
	}
	playlist, channelID, err := s.relay.ServePlaylist(r.Context(), slug)
	if err != nil {
		handleStoreError(w, err)
		return
	}
	if channelID != "" {
		s.relay.RecordViewer(channelID, clientIP(r))
	}
	playlist = appendPlaybackTokenToSegments(playlist, exp, sig)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(playlist))
}

func (s *Server) serveProxyAsset(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		methodNotAllowed(w)
		return
	}
	ref := r.URL.Query().Get("ref")
	sig := r.URL.Query().Get("sig")
	if ref == "" || sig == "" {
		badRequest(w, errors.New("ref and sig are required"))
		return
	}
	asset, channelID, err := s.relay.ServeAsset(r.Context(), slug, ref, sig)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer asset.Close()
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=120")
	http.ServeContent(w, r, asset.Name, asset.ModTime, asset.Reader)
	if channelID != "" {
		s.store.IncrementMetric(r.Context(), channelID, "bytes_sent", asset.Size)
	}
}

func (s *Server) serveTransmuxSegment(w http.ResponseWriter, r *http.Request, slug, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		methodNotAllowed(w)
		return
	}
	if _, _, ok := s.requirePlaybackToken(w, r, slug); !ok {
		return
	}
	asset, channelID, err := s.relay.ServeTransmuxSegment(r.Context(), slug, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer asset.Close()
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=30")
	http.ServeContent(w, r, asset.Name, asset.ModTime, asset.Reader)
	if channelID != "" {
		s.store.IncrementMetric(r.Context(), channelID, "bytes_sent", asset.Size)
	}
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := s.currentSession(r); ok {
		return true
	}
	writeError(w, http.StatusUnauthorized, "admin token required")
	return false
}

// bearerToken extracts the bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

// currentSession returns the authenticated session, validating either the
// env-based service token or a DB-backed user session token.
func (s *Server) currentSession(r *http.Request) (store.Session, bool) {
	token := bearerToken(r)
	if token == "" {
		return store.Session{}, false
	}
	if s.cfg.AdminToken != "" && token == s.cfg.AdminToken {
		return store.Session{Token: token, Username: "service", Role: "admin", ExpiresAt: time.Now().Add(time.Hour)}, true
	}
	if sess, ok := s.auth.get(token); ok {
		return sess, true
	}
	sess, err := s.store.LookupSession(r.Context(), token)
	if err != nil {
		return store.Session{}, false
	}
	s.auth.put(token, sess)
	return sess, true
}

// requirePlaybackToken validates exp+sig query params for /proxy playback.
// Returns the validated values so they can be re-applied to segment URLs.
// Admin bearer token bypasses the check (returns 0, "", true).
func (s *Server) requirePlaybackToken(w http.ResponseWriter, r *http.Request, slug string) (int64, string, bool) {
	if _, ok := s.currentSession(r); ok {
		return 0, "", true
	}
	if ch, err := s.store.GetChannelBySlug(r.Context(), slug); err == nil && !ch.PlaybackTokenRequired {
		return 0, "", true
	}
	expStr := r.URL.Query().Get("exp")
	sig := r.URL.Query().Get("sig")
	if expStr == "" || sig == "" {
		writeError(w, http.StatusForbidden, "playback token required")
		return 0, "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid playback token")
		return 0, "", false
	}
	if time.Now().Unix() > exp {
		writeError(w, http.StatusForbidden, "playback token expired")
		return 0, "", false
	}
	if !s.relay.VerifyPlayback(slug, sig, exp) {
		writeError(w, http.StatusForbidden, "invalid playback token")
		return 0, "", false
	}
	return exp, sig, true
}

// appendPlaybackTokenToSegments adds ?exp=&sig= to every URI line in an
// HLS playlist body so HLS players that don't forward the playlist query
// string still authenticate when fetching segments.
func appendPlaybackTokenToSegments(playlist string, exp int64, sig string) string {
	if exp == 0 || sig == "" {
		return playlist
	}
	query := fmt.Sprintf("exp=%d&sig=%s", exp, url.QueryEscape(sig))
	lines := strings.Split(playlist, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lines[i] = uriAttrPattern.ReplaceAllStringFunc(line, func(match string) string {
				inner := match[len(`URI="`) : len(match)-1]
				return `URI="` + appendQuery(inner, query) + `"`
			})
			continue
		}
		lines[i] = appendQuery(line, query)
	}
	return strings.Join(lines, "\n")
}

func appendQuery(raw, query string) string {
	if strings.Contains(raw, "?") {
		return raw + "&" + query
	}
	return raw + "?" + query
}

var uriAttrPattern = regexp.MustCompile(`URI="([^"]+)"`)

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		origin := strings.TrimRight(r.Header.Get("Origin"), "/")

		// Build the allow-set for this request based on the route:
		//  - stream paths (/proxy, /share, /embed) use DB-driven per-user origins
		//  - everything else (admin UI, /auth, /me, /channels, /admin, /node,
		//    /agent, /health) uses ADMIN_UI_ORIGINS from env
		isStream := strings.HasPrefix(r.URL.Path, "/proxy/") ||
			strings.HasPrefix(r.URL.Path, "/share/") ||
			strings.HasPrefix(r.URL.Path, "/embed/")

		allowSet := map[string]struct{}{}
		if isStream {
			for k := range s.origins.get(s.rootCtx, s.store) {
				allowSet[k] = struct{}{}
			}
		} else {
			for _, o := range s.cfg.AdminUIOrigins {
				allowSet[strings.ToLower(strings.TrimRight(o, "/"))] = struct{}{}
			}
		}

		allow := ""
		if len(allowSet) == 0 {
			// Stream open-by-default when no DB entries; UI never falls back to *
			// unless admin explicitly leaves ADMIN_UI_ORIGINS empty.
			allow = "*"
		} else if origin != "" {
			if _, ok := allowSet[strings.ToLower(origin)]; ok {
				allow = origin
			}
		}
		if allow != "" {
			w.Header().Set("Access-Control-Allow-Origin", allow)
			if allow != "*" {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Range, Origin, Accept, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validateChannelInput(ctx context.Context, input store.ChannelInput) error {
	if strings.TrimSpace(input.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(input.Slug) == "" {
		return errors.New("slug is required")
	}
	if input.Mode != "" && input.Mode != "ingest" && input.Mode != "proxy" && input.Mode != "transmux" {
		return errors.New("mode must be ingest, proxy, or transmux")
	}
	if input.Status != "" && input.Status != "active" && input.Status != "disabled" && input.Status != "source_error" {
		return errors.New("status must be active, disabled, or source_error")
	}
	parsed, err := url.Parse(input.InputURL)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("inputUrl must be a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("inputUrl must use http or https")
	}
	return rejectPrivateHost(ctx, parsed.Hostname())
}

func rejectPrivateHost(ctx context.Context, host string) error {
	if strings.EqualFold(host, "localhost") {
		return errors.New("inputUrl host cannot be localhost")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if isPrivateAddress(addr) {
			return errors.New("inputUrl cannot point to a private or local address")
		}
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return fmt.Errorf("inputUrl host lookup failed: %w", err)
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if ok && isPrivateAddress(addr) {
			return errors.New("inputUrl resolves to a private or local address")
		}
	}
	return nil
}

func isPrivateAddress(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func badRequest(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, err.Error())
}

func serverError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusInternalServerError, err.Error())
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func handleStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return rip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handlePlaybackToken(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	channel, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		handleStoreError(w, err)
		return
	}
	ttl := s.cfg.PlaybackTokenTTLSecs
	if ttl <= 0 {
		ttl = 900
	}
	exp := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
	sig := s.relay.SignPlayback(channel.Slug, exp)
	base := strings.TrimRight(s.cfg.PublicStreamURL, "/")
	playbackURL := fmt.Sprintf("%s/proxy/%s/index.m3u8?exp=%d&sig=%s",
		base, url.PathEscape(channel.Slug), exp, url.QueryEscape(sig))
	writeJSON(w, http.StatusOK, map[string]any{
		"playbackUrl": playbackURL,
		"expiresAt":   time.Unix(exp, 0).UTC().Format(time.RFC3339),
		"ttlSeconds":  ttl,
	})
}

func (s *Server) handleShareLink(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body struct {
		TtlMinutes int `json:"ttlMinutes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.TtlMinutes <= 0 {
		body.TtlMinutes = 240
	}
	if body.TtlMinutes > 60*24*30 {
		body.TtlMinutes = 60 * 24 * 30
	}
	channel, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		handleStoreError(w, err)
		return
	}
	exp := time.Now().Add(time.Duration(body.TtlMinutes) * time.Minute).Unix()
	sig := s.relay.SignPlayback(channel.Slug, exp)
	base := strings.TrimRight(s.cfg.PublicStreamURL, "/")
	playlistURL := fmt.Sprintf("%s/share/%s/index.m3u8?exp=%d&sig=%s", base, url.PathEscape(channel.Slug), exp, sig)
	embedURL := fmt.Sprintf("%s/embed/%s?exp=%d&sig=%s", base, url.PathEscape(channel.Slug), exp, sig)
	writeJSON(w, http.StatusOK, map[string]any{
		"playlistUrl": playlistURL,
		"embedUrl":    embedURL,
		"expiresAt":   time.Unix(exp, 0).UTC().Format(time.RFC3339),
	})
}

func (s *Server) share(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/share/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	slug := parts[0]
	expStr := r.URL.Query().Get("exp")
	sig := r.URL.Query().Get("sig")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || sig == "" {
		writeError(w, http.StatusForbidden, "missing or invalid signature")
		return
	}
	if time.Now().Unix() > exp {
		writeError(w, http.StatusForbidden, "share link expired")
		return
	}
	if !s.relay.VerifyPlayback(slug, sig, exp) {
		writeError(w, http.StatusForbidden, "invalid signature")
		return
	}
	if parts[1] == "index.m3u8" {
		playlist, channelID, err := s.relay.ServePlaylist(r.Context(), slug)
		if err != nil {
			handleStoreError(w, err)
			return
		}
		if channelID != "" {
			s.relay.RecordViewer(channelID, clientIP(r))
		}
		playlist = appendPlaybackTokenToSegments(playlist, exp, sig)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(playlist))
		return
	}
	if parts[1] == "segment" && len(parts) == 3 && parts[2] != "" {
		asset, channelID, err := s.relay.ServeTransmuxSegment(r.Context(), slug, parts[2])
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		defer asset.Close()
		w.Header().Set("Content-Type", asset.ContentType)
		w.Header().Set("Cache-Control", "public, max-age=30")
		http.ServeContent(w, r, asset.Name, asset.ModTime, asset.Reader)
		if channelID != "" {
			s.store.IncrementMetric(r.Context(), channelID, "bytes_sent", asset.Size)
		}
		return
	}
	if parts[1] == "asset" {
		s.serveProxyAsset(w, r, slug)
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

func (s *Server) embed(w http.ResponseWriter, r *http.Request) {
	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/embed/"), "/")
	if slug == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	exp := r.URL.Query().Get("exp")
	sig := r.URL.Query().Get("sig")
	base := strings.TrimRight(s.cfg.PublicStreamURL, "/")
	playlistURL := fmt.Sprintf("%s/share/%s/index.m3u8?exp=%s&sig=%s",
		base, url.PathEscape(slug), url.QueryEscape(exp), url.QueryEscape(sig))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, embedHTMLTemplate, slug, playlistURL)
}

const embedHTMLTemplate = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>%[1]s</title>
<style>html,body{margin:0;height:100%%;background:#000}video{width:100%%;height:100%%;object-fit:contain;display:block}</style>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1"></script>
</head><body>
<video id="v" controls autoplay muted playsinline></video>
<script>
const url=%[2]q;const v=document.getElementById('v');
if(window.Hls&&Hls.isSupported()){const h=new Hls({liveSyncDuration:6});h.loadSource(url);h.attachMedia(v);}
else if(v.canPlayType('application/vnd.apple.mpegurl')){v.src=url;}
else{document.body.innerHTML='<p style=color:#fff;font-family:sans-serif;padding:1rem>HLS not supported</p>';}
</script></body></html>`
