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
}

func NewServer(cfg config.Config, store *store.Store, relayManager *relay.Manager, logger *slog.Logger) http.Handler {
	server := &Server{
		cfg:     cfg,
		store:   store,
		relay:   relayManager,
		logger:  logger,
		router:  http.NewServeMux(),
		rootCtx: context.Background(),
	}
	server.routes()
	return server.withCORS(server.router)
}

func (s *Server) routes() {
	s.router.HandleFunc("/health", s.health)
	s.router.HandleFunc("/auth/login", s.login)
	s.router.HandleFunc("/channels", s.channels)
	s.router.HandleFunc("/channels/", s.channelByID)
	s.router.HandleFunc("/proxy/", s.proxy)
	s.router.HandleFunc("/share/", s.share)
	s.router.HandleFunc("/embed/", s.embed)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	if s.cfg.AdminToken == "" {
		writeJSON(w, http.StatusOK, map[string]any{"token": "", "authEnabled": false})
		return
	}
	if body.Token != "" && body.Token == s.cfg.AdminToken {
		writeJSON(w, http.StatusOK, map[string]any{"token": s.cfg.AdminToken, "authEnabled": s.cfg.AdminToken != ""})
		return
	}
	if body.Username == s.cfg.AdminUsername && body.Password == s.cfg.AdminPassword {
		writeJSON(w, http.StatusOK, map[string]any{"token": s.cfg.AdminToken, "authEnabled": true})
		return
	}
	writeError(w, http.StatusUnauthorized, "invalid username or password")
}

func (s *Server) channels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		channels, err := s.store.ListChannels(r.Context())
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
		channel, err := s.store.CreateChannel(r.Context(), input, s.cfg.PublicStreamURL)
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
	if !s.requireAdmin(w, r) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/channels/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	id := parts[0]
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
	playlist, channelID, err := s.relay.ServePlaylist(r.Context(), slug)
	if err != nil {
		handleStoreError(w, err)
		return
	}
	if channelID != "" {
		s.relay.RecordViewer(channelID, clientIP(r))
	}
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
	if s.cfg.AdminToken == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+s.cfg.AdminToken {
		return true
	}
	writeError(w, http.StatusUnauthorized, "admin token required")
	return false
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
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
	if parts[1] == "index.m3u8" {
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
		s.serveProxyPlaylist(w, r, slug)
		return
	}
	if parts[1] == "segment" && len(parts) == 3 && parts[2] != "" {
		s.serveTransmuxSegment(w, r, slug, parts[2])
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
