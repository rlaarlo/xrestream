package nodeagent

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"restream/backend/internal/relay"
)

// httpServer is a slim HTTP layer for a node-agent that serves only the
// /proxy/* playback endpoints (no admin / no auth surface). Playback
// tokens are verified via the signing secret that the control plane
// sends in /node/config so share-links minted on the control plane work
// transparently here.
type httpServer struct {
	relay  *relay.Manager
	store  *memStore
	logger *slog.Logger
}

func newHTTPServer(rm *relay.Manager, store *memStore, logger *slog.Logger) http.Handler {
	h := &httpServer{relay: rm, store: store, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/proxy/", h.proxy)
	return withCORS(mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *httpServer) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok","role":"node"}`))
}

func (h *httpServer) proxy(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/proxy/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	slug := parts[0]
	switch {
	case parts[1] == "index.m3u8":
		h.servePlaylist(w, r, slug)
	case parts[1] == "asset":
		h.serveAsset(w, r, slug)
	case parts[1] == "segment" && len(parts) == 3 && parts[2] != "":
		h.serveTransmuxSegment(w, r, slug, parts[2])
	default:
		http.NotFound(w, r)
	}
}

func (h *httpServer) servePlaylist(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	exp, sig, ok := h.requirePlaybackToken(w, r, slug)
	if !ok {
		return
	}
	playlist, channelID, err := h.relay.ServePlaylist(r.Context(), slug)
	if err != nil {
		h.writePlaybackError(w, err)
		return
	}
	if channelID != "" {
		h.relay.RecordViewer(channelID, clientIP(r))
	}
	playlist = appendPlaybackTokenToSegments(playlist, exp, sig)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(playlist))
}

func (h *httpServer) serveAsset(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ref := r.URL.Query().Get("ref")
	sig := r.URL.Query().Get("sig")
	if ref == "" || sig == "" {
		http.Error(w, "ref and sig are required", http.StatusBadRequest)
		return
	}
	asset, _, err := h.relay.ServeAsset(r.Context(), slug, ref, sig)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer asset.Close()
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=120")
	http.ServeContent(w, r, asset.Name, asset.ModTime, asset.Reader)
}

func (h *httpServer) serveTransmuxSegment(w http.ResponseWriter, r *http.Request, slug, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodOptions {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, ok := h.requirePlaybackToken(w, r, slug); !ok {
		return
	}
	asset, _, err := h.relay.ServeTransmuxSegment(r.Context(), slug, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer asset.Close()
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=30")
	http.ServeContent(w, r, asset.Name, asset.ModTime, asset.Reader)
}

func (h *httpServer) writePlaybackError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusServiceUnavailable)
}

// requirePlaybackToken validates exp+sig query params using the signer
// configured on the relay manager. Tokens minted on the control plane
// are verified here because both share the same SIGNING_SECRET.
func (h *httpServer) requirePlaybackToken(w http.ResponseWriter, r *http.Request, slug string) (int64, string, bool) {
	channel, err := h.store.GetChannelBySlug(r.Context(), slug)
	if err == nil && !channel.PlaybackTokenRequired {
		return 0, "", true
	}
	expStr := r.URL.Query().Get("exp")
	sig := r.URL.Query().Get("sig")
	if expStr == "" || sig == "" {
		http.Error(w, "playback token required", http.StatusForbidden)
		return 0, "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid playback token", http.StatusForbidden)
		return 0, "", false
	}
	if time.Now().Unix() > exp {
		http.Error(w, "playback token expired", http.StatusForbidden)
		return 0, "", false
	}
	if !h.relay.VerifyPlayback(slug, sig, exp) {
		http.Error(w, "invalid playback token", http.StatusForbidden)
		return 0, "", false
	}
	return exp, sig, true
}

var uriAttrPattern = regexp.MustCompile(`URI="([^"]+)"`)

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

func clientIP(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if idx := strings.Index(h, ","); idx > 0 {
			return strings.TrimSpace(h[:idx])
		}
		return strings.TrimSpace(h)
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		return strings.TrimSpace(h)
	}
	if r.RemoteAddr != "" {
		if idx := strings.LastIndex(r.RemoteAddr, ":"); idx > 0 {
			return r.RemoteAddr[:idx]
		}
		return r.RemoteAddr
	}
	return ""
}
