package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"restream/backend/internal/config"
	"restream/backend/internal/store"
)

type Manager struct {
	store  ChannelStore
	cfg    config.Config
	logger *slog.Logger
	client *http.Client
	signer Signer
	cache  DiskCache
	r2     *R2Client

	mu        sync.RWMutex
	playlists map[string]cachedPlaylist
	transmux  map[string]cachedPlaylist
	assets    map[string]map[string]string
	workers   map[string]*workerHandle
	group     singleflight.Group
	r2Ready   sync.Map

	viewersMu sync.Mutex
	viewers   map[string]map[string]time.Time // channelID -> ip -> lastSeen
}

type cachedPlaylist struct {
	body      string
	expiresAt time.Time
}

type workerHandle struct {
	cancel context.CancelFunc
}

func NewManager(store ChannelStore, cfg config.Config, logger *slog.Logger, r2 *R2Client) *Manager {
	timeout := time.Duration(cfg.UpstreamTimeoutMS) * time.Millisecond
	// Reuse TCP+TLS for upstream playlist/segment fetches. Without this every
	// 2-6s playlist poll opens a fresh socket — single biggest source of
	// per-request latency at the relay's hot path.
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   16,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &Manager{
		store:     store,
		cfg:       cfg,
		logger:    logger,
		client:    &http.Client{Timeout: timeout, Transport: transport},
		signer:    NewSigner(cfg.SigningSecret),
		cache:     NewDiskCache(cfg.CacheDir),
		r2:        r2,
		playlists: map[string]cachedPlaylist{},
		transmux:  map[string]cachedPlaylist{},
		assets:    map[string]map[string]string{},
		workers:   map[string]*workerHandle{},
		viewers:   map[string]map[string]time.Time{},
	}
}

// SetR2Client swaps the R2 client used for segment uploads at runtime. Pass
// nil to disable R2 sync. Used by the node-agent when the control plane
// pushes updated R2 credentials.
func (m *Manager) SetR2Client(r2 *R2Client) {
	m.mu.Lock()
	m.r2 = r2
	// Clear the per-asset readiness map so new uploads are attempted with
	// the new client/bucket.
	m.r2Ready = sync.Map{}
	m.mu.Unlock()
}

func (m *Manager) RegisterAsset(slug, sourceURL string) (string, string) {
	ref := m.signer.Ref(sourceURL)
	sig := m.signer.Sign(ref)
	m.mu.Lock()
	if m.assets[slug] == nil {
		m.assets[slug] = map[string]string{}
	}
	m.assets[slug][ref] = sourceURL
	m.mu.Unlock()
	if m.r2 != nil {
		if _, ready := m.r2Ready.Load(slug + ":" + ref); ready {
			return ref, m.r2.PublicURL(slug, ref)
		}
	}
	return ref, fmt.Sprintf("/proxy/%s/asset?ref=%s&sig=%s", url.PathEscape(slug), ref, sig)
}

func (m *Manager) StartActiveWorkers(ctx context.Context) error {
	channels, err := m.store.ActiveWorkerChannels(ctx)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		m.StartWorker(ctx, channel)
	}
	go m.cleanupLoop(ctx)
	return nil
}

func (m *Manager) StartWorker(parent context.Context, channel store.Channel) {
	if channel.Status != "active" {
		return
	}
	if channel.Mode != "ingest" && channel.Mode != "transmux" {
		return
	}

	m.mu.Lock()
	if _, exists := m.workers[channel.ID]; exists {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	handle := &workerHandle{cancel: cancel}
	m.workers[channel.ID] = handle
	m.mu.Unlock()

	if err := m.store.SetWorkerStatus(parent, channel.ID, "running", nil); err != nil {
		slog.Warn("relay: set worker status running", "slug", channel.Slug, "err", err)
	}

	if channel.Mode == "transmux" {
		go m.transmuxLoop(ctx, channel, handle)
		return
	}
	go m.workerLoop(ctx, channel, handle)
}

func (m *Manager) StopWorker(channelID string) {
	m.mu.Lock()
	handle, exists := m.workers[channelID]
	if exists {
		handle.cancel()
		delete(m.workers, channelID)
	}
	m.mu.Unlock()
}

// releaseWorker removes the worker entry only if it's still the same handle.
// Used by worker goroutines in defer so a stale goroutine cannot cancel a
// freshly-started successor (PATCH calls StopWorker+StartWorker in sequence).
func (m *Manager) releaseWorker(channelID string, h *workerHandle) {
	m.mu.Lock()
	if cur, ok := m.workers[channelID]; ok && cur == h {
		delete(m.workers, channelID)
	}
	m.mu.Unlock()
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	for id, handle := range m.workers {
		handle.cancel()
		delete(m.workers, id)
	}
	m.mu.Unlock()
}

func (m *Manager) WorkerRunning(channelID string) bool {
	m.mu.RLock()
	_, ok := m.workers[channelID]
	m.mu.RUnlock()
	return ok
}

// RecordViewer marks an IP as actively watching the channel (per-playlist hit).
func (m *Manager) RecordViewer(channelID, ip string) {
	if channelID == "" || ip == "" {
		return
	}
	m.viewersMu.Lock()
	defer m.viewersMu.Unlock()
	bucket, ok := m.viewers[channelID]
	if !ok {
		bucket = map[string]time.Time{}
		m.viewers[channelID] = bucket
	}
	bucket[ip] = time.Now()
}

// LiveViewers counts distinct IPs that requested the playlist within 60 seconds.
func (m *Manager) LiveViewers(channelID string) int {
	cutoff := time.Now().Add(-60 * time.Second)
	m.viewersMu.Lock()
	defer m.viewersMu.Unlock()
	bucket, ok := m.viewers[channelID]
	if !ok {
		return 0
	}
	for ip, t := range bucket {
		if t.Before(cutoff) {
			delete(bucket, ip)
		}
	}
	if len(bucket) == 0 {
		delete(m.viewers, channelID)
		return 0
	}
	return len(bucket)
}

// SignPlayback / VerifyPlayback expose the signer for share links.
func (m *Manager) SignPlayback(slug string, exp int64) string {
	return m.signer.SignPlayback(slug, exp)
}

func (m *Manager) VerifyPlayback(slug, sig string, exp int64) bool {
	return m.signer.VerifyPlayback(slug, sig, exp)
}

func (m *Manager) ServePlaylist(ctx context.Context, slug string) (string, string, error) {
	channel, err := m.store.GetChannelBySlug(ctx, slug)
	if err != nil {
		return "", "", err
	}
	m.store.TouchRequest(ctx, channel.ID)
	m.store.IncrementMetric(ctx, channel.ID, "playlist_requests", 1)

	if channel.Status != "active" && channel.Status != "source_error" {
		return "", "", errors.New("channel is disabled")
	}

	if channel.Mode == "transmux" {
		// 1s in-memory cache + singleflight: collapses N concurrent viewer
		// polls of the same channel into a single disk read of index.m3u8.
		m.mu.RLock()
		cached, ok := m.transmux[channel.Slug]
		m.mu.RUnlock()
		if ok && time.Now().Before(cached.expiresAt) {
			return cached.body, channel.ID, nil
		}
		v, err, _ := m.group.Do("transmux:"+channel.Slug, func() (any, error) {
			m.mu.RLock()
			if c, ok := m.transmux[channel.Slug]; ok && time.Now().Before(c.expiresAt) {
				m.mu.RUnlock()
				return c.body, nil
			}
			m.mu.RUnlock()
			pl, err := m.readTransmuxPlaylist(channel)
			if err != nil {
				return "", err
			}
			m.mu.Lock()
			m.transmux[channel.Slug] = cachedPlaylist{body: pl, expiresAt: time.Now().Add(time.Second)}
			m.mu.Unlock()
			return pl, nil
		})
		if err != nil {
			return "", channel.ID, err
		}
		return v.(string), channel.ID, nil
	}

	if channel.Mode == "ingest" {
		m.mu.RLock()
		cached, ok := m.playlists[channel.Slug]
		m.mu.RUnlock()
		if !ok || cached.body == "" {
			return "", channel.ID, errors.New("ingest playlist is not ready yet")
		}
		return cached.body, channel.ID, nil
	}

	m.mu.RLock()
	cached, ok := m.playlists[channel.Slug]
	m.mu.RUnlock()
	if ok && time.Now().Before(cached.expiresAt) {
		return cached.body, channel.ID, nil
	}

	body, statusCode, err := m.fetchTextWithHeaders(ctx, channel.InputURL, channel.HTTPReferer, channel.HTTPUserAgent, channel.HTTPOrigin)
	if err != nil {
		message := err.Error()
		_ = m.store.SetSourceStatus(ctx, channel.ID, statusCode, &message)
		return "", channel.ID, err
	}
	_ = m.store.SetSourceStatus(ctx, channel.ID, statusCode, nil)
	result, err := RewritePlaylist(body, channel.InputURL, channel.Slug, m)
	if err != nil {
		return "", channel.ID, err
	}

	m.mu.Lock()
	m.playlists[channel.Slug] = cachedPlaylist{body: result.Playlist, expiresAt: time.Now().Add(time.Duration(channel.PlaylistTTLSeconds) * time.Second)}
	m.mu.Unlock()
	return result.Playlist, channel.ID, nil
}

func (m *Manager) ServeAsset(ctx context.Context, slug, ref, sig string) (*osFileResponse, string, error) {
	if !m.signer.Verify(ref, sig) {
		return nil, "", errors.New("invalid asset signature")
	}
	channel, err := m.store.GetChannelBySlug(ctx, slug)
	if err != nil {
		return nil, "", err
	}
	m.store.TouchRequest(ctx, channel.ID)
	m.store.IncrementMetric(ctx, channel.ID, "segment_requests", 1)

	sourceURL := m.assetSource(slug, ref)
	if sourceURL == "" {
		return nil, channel.ID, errors.New("asset reference is unknown")
	}

	if m.cache.HasFresh(slug, ref, channel.SegmentTTLSeconds) {
		m.store.IncrementMetric(ctx, channel.ID, "cache_hits", 1)
		return m.openCachedAsset(slug, ref, sourceURL, channel.ID)
	}

	m.store.IncrementMetric(ctx, channel.ID, "cache_misses", 1)
	if channel.Mode == "ingest" {
		// Wait briefly for the ingest worker (or another viewer's fetch) to
		// populate the cache. Capping at 1.5s keeps a stalled source from
		// stalling the player connection for 5s — the HLS client retries
		// segments with its own backoff which is smoother than holding here.
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if m.cache.HasFresh(slug, ref, channel.SegmentTTLSeconds) {
				return m.openCachedAsset(slug, ref, sourceURL, channel.ID)
			}
			select {
			case <-ctx.Done():
				return nil, channel.ID, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		return nil, channel.ID, errors.New("asset is not cached yet")
	}

	_, err, _ = m.group.Do(slug+":"+ref, func() (any, error) {
		return nil, m.fetchAndCache(ctx, channel, ref, sourceURL)
	})
	if err != nil {
		return nil, channel.ID, err
	}
	return m.openCachedAsset(slug, ref, sourceURL, channel.ID)
}

type osFileResponse struct {
	Reader      io.ReadSeeker
	ModTime     time.Time
	Size        int64
	Name        string
	ContentType string
	Close       func() error
}

func (m *Manager) openCachedAsset(slug, ref, sourceURL, channelID string) (*osFileResponse, string, error) {
	file, info, err := m.cache.Open(slug, ref)
	if err != nil {
		return nil, channelID, err
	}
	return &osFileResponse{
		Reader:      file,
		ModTime:     info.ModTime(),
		Size:        info.Size(),
		Name:        path.Base(sourceURL),
		ContentType: contentTypeForURL(sourceURL),
		Close:       file.Close,
	}, channelID, nil
}

func (m *Manager) PurgeChannel(slug string) error {
	m.mu.Lock()
	delete(m.playlists, slug)
	delete(m.transmux, slug)
	delete(m.assets, slug)
	m.mu.Unlock()
	prefix := slug + ":"
	m.r2Ready.Range(func(k, v any) bool {
		if ks, ok := k.(string); ok && strings.HasPrefix(ks, prefix) {
			m.r2Ready.Delete(k)
		}
		return true
	})
	if m.r2 != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := m.r2.DeleteFolder(ctx, slug); err != nil {
				m.logger.Warn("r2: delete folder failed", "slug", slug, "error", err)
			}
		}()
	}
	_ = os.RemoveAll(m.transmuxDir(slug))
	return m.cache.PurgeChannel(slug)
}

func (m *Manager) assetSource(slug, ref string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.assets[slug][ref]
}

func (m *Manager) workerLoop(ctx context.Context, channel store.Channel, handle *workerHandle) {
	logger := m.logger.With("channel", channel.Slug, "channelId", channel.ID)
	_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "running", nil)
	defer func() {
		m.releaseWorker(channel.ID, handle)
		_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "stopped", nil)
	}()

	poll := time.Duration(channel.IngestPollSeconds) * time.Second
	if poll <= 0 {
		poll = 2 * time.Second
	}

	for {
		if err := m.runIngestCycle(ctx, channel); err != nil {
			message := err.Error()
			_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "error", &message)
			_ = m.store.SetSourceStatus(context.Background(), channel.ID, 0, &message)
			m.store.IncrementMetric(context.Background(), channel.ID, "worker_errors", 1)
			logger.Warn("ingest cycle failed", "error", err)
		} else {
			_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "running", nil)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

func (m *Manager) runIngestCycle(ctx context.Context, channel store.Channel) error {
	body, statusCode, err := m.fetchTextWithHeaders(ctx, channel.InputURL, channel.HTTPReferer, channel.HTTPUserAgent, channel.HTTPOrigin)
	if err != nil {
		return err
	}
	_ = m.store.SetSourceStatus(ctx, channel.ID, statusCode, nil)

	result, err := RewritePlaylist(body, channel.InputURL, channel.Slug, m)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.playlists[channel.Slug] = cachedPlaylist{body: result.Playlist, expiresAt: time.Now().Add(24 * time.Hour)}
	m.mu.Unlock()

	for _, sourceURL := range result.Assets {
		ref, _ := m.RegisterAsset(channel.Slug, sourceURL)
		if m.cache.HasFresh(channel.Slug, ref, channel.SegmentTTLSeconds) {
			continue
		}
		go func(ref, sourceURL string) {
			if err := m.fetchAndCache(context.Background(), channel, ref, sourceURL); err != nil {
				m.logger.Warn("segment prefetch failed", "channel", channel.Slug, "error", err)
			}
		}(ref, sourceURL)
	}
	return nil
}

func (m *Manager) fetchText(ctx context.Context, rawURL string) (string, int, error) {
	return m.fetchTextWithHeaders(ctx, rawURL, "", "", "")
}

func (m *Manager) fetchTextWithHeaders(ctx context.Context, rawURL, referer, userAgent, origin string) (string, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	applyUpstreamHeaders(request, referer, userAgent, origin)
	response, err := m.client.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", response.StatusCode, fmt.Errorf("upstream returned %s", response.Status)
	}
	limited := io.LimitReader(response.Body, 4*1024*1024)
	bytes, err := io.ReadAll(limited)
	if err != nil {
		return "", response.StatusCode, err
	}
	return string(bytes), response.StatusCode, nil
}

func applyUpstreamHeaders(request *http.Request, referer, userAgent, origin string) {
	if userAgent = strings.TrimSpace(userAgent); userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	} else {
		request.Header.Set("User-Agent", "restream-hls-relay/0.1")
	}
	if referer = strings.TrimSpace(referer); referer != "" {
		request.Header.Set("Referer", referer)
	}
	if origin = strings.TrimSpace(origin); origin != "" {
		request.Header.Set("Origin", origin)
	}
}

func (m *Manager) fetchAndCache(ctx context.Context, channel store.Channel, ref, sourceURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	applyUpstreamHeaders(request, channel.HTTPReferer, channel.HTTPUserAgent, channel.HTTPOrigin)
	response, err := m.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("upstream asset returned %s", response.Status)
	}
	m.store.IncrementMetric(ctx, channel.ID, "upstream_requests", 1)
	written, err := m.cache.Write(channel.Slug, ref, response.Body)
	if err != nil {
		return err
	}
	m.store.IncrementMetric(ctx, channel.ID, "bytes_upstream", written)
	if channel.SyncEnabled && m.r2 != nil {
		go m.uploadToR2(channel.Slug, ref, sourceURL)
	}
	return nil
}

func (m *Manager) uploadToR2(slug, ref, sourceURL string) {
	if _, exists := m.r2Ready.Load(slug + ":" + ref); exists {
		return
	}
	file, _, err := m.cache.Open(slug, ref)
	if err != nil {
		m.logger.Warn("r2: open cache failed", "slug", slug, "ref", ref, "error", err)
		return
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.r2.Upload(ctx, slug, ref, file, contentTypeForURL(sourceURL)); err != nil {
		m.logger.Warn("r2: upload failed", "slug", slug, "ref", ref, "error", err)
		return
	}
	m.r2Ready.Store(slug+":"+ref, true)
}

func (m *Manager) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.cache.Cleanup(6 * time.Hour); err != nil {
				m.logger.Warn("cache cleanup failed", "error", err)
			}
		}
	}
}

// r2RetentionLoop deletes R2 objects for a channel older than the configured
// retention window so live-stream segments don't pile up indefinitely.
func (m *Manager) r2RetentionLoop(ctx context.Context, channel store.Channel) {
	if m.r2 == nil {
		return
	}
	retention := time.Duration(m.cfg.R2RetentionSecs) * time.Second
	if retention <= 0 {
		return
	}
	logger := m.logger.With("channel", channel.Slug, "channelId", channel.ID)
	logger.Info("r2: retention loop started", "retention", retention.String())
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		keys, err := m.r2.ListOlderThan(listCtx, channel.Slug, retention)
		cancel()
		if err != nil {
			logger.Warn("r2: retention list failed", "error", err)
			continue
		}
		if len(keys) == 0 {
			continue
		}
		delCtx, cancelDel := context.WithTimeout(ctx, 60*time.Second)
		if err := m.r2.DeleteKeys(delCtx, keys); err != nil {
			logger.Warn("r2: retention delete failed", "count", len(keys), "error", err)
		} else {
			logger.Info("r2: retention deleted", "count", len(keys))
			// Forget r2Ready entries so the keys can be re-uploaded if ffmpeg
			// somehow recreates them (shouldn't happen but cheap insurance).
			for _, key := range keys {
				parts := strings.SplitN(key, "/", 2)
				if len(parts) == 2 {
					m.r2Ready.Delete(parts[0] + ":" + parts[1])
				}
			}
		}
		cancelDel()
	}
}

func contentTypeForURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "application/octet-stream"
	}
	ext := strings.ToLower(path.Ext(parsed.Path))
	if ext == ".m3u8" {
		return "application/vnd.apple.mpegurl"
	}
	if ext == ".ts" {
		return "video/mp2t"
	}
	if ext == ".m4s" {
		return "video/iso.segment"
	}
	if ext == ".key" {
		return "application/octet-stream"
	}
	if guessed := mime.TypeByExtension(ext); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

var ErrNotFound = pgx.ErrNoRows

func (m *Manager) transmuxDir(slug string) string {
	return filepath.Join(m.cfg.CacheDir, "transmux", slug)
}

func (m *Manager) readTransmuxPlaylist(channel store.Channel) (string, error) {
	dir := m.transmuxDir(channel.Slug)
	playlistPath := filepath.Join(dir, "index.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("transmux playlist is not ready yet")
		}
		return "", err
	}

	rawLines := strings.Split(string(data), "\n")

	// Sync mode: trim segments newer than wall-clock cutoff so every viewer
	// observes the exact same playlist tail at the same instant.
	var cutoff time.Time
	syncOn := channel.SyncEnabled
	if syncOn {
		delay := time.Duration(channel.SyncDelaySeconds) * time.Second
		if delay <= 0 {
			delay = 8 * time.Second
		}
		// Quantize to whole seconds so concurrent viewers compute identical cutoff.
		cutoff = time.Now().Truncate(time.Second).Add(-delay)
	}

	var (
		out             []string
		pendingHeader   []string
		mediaSequence   int64 = -1
		droppedFromHead int64
		startInjected   bool
	)

	flushHeader := func() {
		if len(pendingHeader) > 0 {
			out = append(out, pendingHeader...)
			pendingHeader = nil
		}
	}

	for index := 0; index < len(rawLines); index++ {
		line := rawLines[index]
		trimmed := strings.TrimSpace(line)

		if !startInjected && trimmed == "#EXTM3U" {
			out = append(out, "#EXTM3U", "#EXT-X-START:TIME-OFFSET=-6,PRECISE=YES")
			startInjected = true
			continue
		}

		if strings.HasPrefix(trimmed, "#EXT-X-MEDIA-SEQUENCE:") {
			fmt.Sscanf(trimmed, "#EXT-X-MEDIA-SEQUENCE:%d", &mediaSequence)
			out = append(out, line) // placeholder, fix later
			continue
		}

		if strings.HasPrefix(trimmed, "#EXTINF:") {
			// Look ahead for the segment line.
			pendingHeader = append(pendingHeader, line)
			continue
		}

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if len(pendingHeader) > 0 {
				pendingHeader = append(pendingHeader, line)
				continue
			}
			out = append(out, line)
			continue
		}

		// segment URI line
		if syncOn {
			info, statErr := os.Stat(filepath.Join(dir, trimmed))
			if statErr != nil || info.ModTime().After(cutoff) {
				// Drop this segment + its EXTINF header (and any tags between).
				if mediaSequence >= 0 && len(out) > 0 && len(pendingHeader) == 0 {
					// already rendered earlier? n/a; we always drop from head only when
					// we have not yet seen a kept segment.
				}
				if len(out) == 0 || allOutputAreHeaders(out) {
					droppedFromHead++
				}
				pendingHeader = nil
				continue
			}
		}
		flushHeader()
		segURL := fmt.Sprintf("segment/%s", trimmed)
		if m.r2 != nil && channel.SyncEnabled {
			if _, ready := m.r2Ready.Load(channel.Slug + ":" + trimmed); ready {
				segURL = m.r2.PublicURL(channel.Slug, trimmed)
			}
		}
		out = append(out, segURL)
	}

	// Adjust MEDIA-SEQUENCE only when we trimmed from the front.
	if syncOn && mediaSequence >= 0 && droppedFromHead > 0 {
		for i, line := range out {
			if strings.HasPrefix(strings.TrimSpace(line), "#EXT-X-MEDIA-SEQUENCE:") {
				out[i] = fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", mediaSequence+droppedFromHead)
				break
			}
		}
	}

	return strings.Join(out, "\n"), nil
}

// allOutputAreHeaders reports whether every line accumulated so far is a
// playlist header rather than a segment URI.
func allOutputAreHeaders(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			return false
		}
	}
	return true
}

func (m *Manager) ServeTransmuxSegment(ctx context.Context, slug, name string) (*osFileResponse, string, error) {
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return nil, "", errors.New("invalid segment name")
	}
	channel, err := m.store.GetChannelBySlug(ctx, slug)
	if err != nil {
		return nil, "", err
	}
	m.store.IncrementMetric(ctx, channel.ID, "segment_requests", 1)

	segmentPath := filepath.Join(m.transmuxDir(slug), name)
	file, err := os.Open(segmentPath)
	if err != nil {
		return nil, channel.ID, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, channel.ID, err
	}
	return &osFileResponse{
		Reader:      file,
		ModTime:     info.ModTime(),
		Size:        info.Size(),
		Name:        name,
		ContentType: contentTypeForURL(name),
		Close:       file.Close,
	}, channel.ID, nil
}

// transmuxR2Watcher scans the transmux dir for new .ts segments and uploads
// them to R2. Marks them ready so the playlist rewriter can emit the public
// R2 URL instead of the local /proxy path.
func (m *Manager) transmuxR2Watcher(ctx context.Context, channel store.Channel, dir string) {
	if m.r2 == nil {
		return
	}
	logger := m.logger.With("channel", channel.Slug, "channelId", channel.ID, "mode", "transmux")
	logger.Info("r2: transmux watcher started", "dir", dir)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			logger.Warn("r2: readdir failed", "error", err)
			continue
		}
		uploaded := 0
		skipped := 0
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".ts") {
				continue
			}
			key := channel.Slug + ":" + name
			if _, exists := m.r2Ready.Load(key); exists {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			// Only upload segments that finished writing >=2s ago to avoid
			// racing ffmpeg's still-open file handle.
			if time.Since(info.ModTime()) < 2*time.Second {
				skipped++
				continue
			}
			path := filepath.Join(dir, name)
			f, err := os.Open(path)
			if err != nil {
				logger.Warn("r2: open segment failed", "name", name, "error", err)
				continue
			}
			upCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = m.r2.Upload(upCtx, channel.Slug, name, f, "video/mp2t")
			cancel()
			_ = f.Close()
			if err != nil {
				logger.Warn("r2: transmux upload failed", "name", name, "error", err)
				continue
			}
			m.r2Ready.Store(key, true)
			uploaded++

			// Delete the local file once it's older than the configured
			// retention window — viewers fetching the playlist now get the
			// R2 URL directly, so the local copy is dead weight. Keep a
			// small grace window so any in-flight local fetch can finish.
			localRetention := time.Duration(m.cfg.LocalRetentionSecs) * time.Second
			if localRetention > 0 && time.Since(info.ModTime()) > localRetention {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					logger.Warn("r2: local cleanup failed", "name", name, "error", err)
				}
			}
		}
		if uploaded > 0 {
			logger.Info("r2: transmux uploaded", "count", uploaded, "skipped", skipped)
		}
	}
}

func (m *Manager) transmuxLoop(ctx context.Context, channel store.Channel, handle *workerHandle) {
	logger := m.logger.With("channel", channel.Slug, "channelId", channel.ID, "mode", "transmux")
	_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "running", nil)
	defer func() {
		m.releaseWorker(channel.ID, handle)
		_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "stopped", nil)
	}()

	dir := m.transmuxDir(channel.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		message := err.Error()
		_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "error", &message)
		return
	}
	playlistPath := filepath.Join(dir, "index.m3u8")

	if m.r2 != nil && channel.SyncEnabled {
		go m.transmuxR2Watcher(ctx, channel, dir)
		go m.r2RetentionLoop(ctx, channel)
	}

	// Track consecutive ffmpeg failures so we can auto-disable a channel
	// whose upstream is broken. Avoids hammering the source and getting banned.
	var firstFailureAt time.Time
	sourceErrorAfter := time.Duration(m.cfg.SourceErrorAfterSecs) * time.Second

	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return
		}

		_ = clearTransmuxFiles(dir)

		startNumber := time.Now().Unix()
		ffmpegArgs := []string{
			"-hide_banner",
			"-loglevel", "info",
			// genpts: regenerate missing PTS; igndts: ignore broken DTS coming from source.
			"-fflags", "+genpts+igndts",
			"-err_detect", "ignore_err",
			"-rw_timeout", "30000000",
			// Let the Go manager restart ffmpeg on source EOF/network error.
			// In-process -reconnect_at_eof/-reconnect_on_* trigger a seek-based
			// HTTP reconnect that can segfault on chunked-encoding edge cases.
		}
		userAgent := strings.TrimSpace(channel.HTTPUserAgent)
		if userAgent == "" {
			userAgent = "VLC/3.0.20 LibVLC/3.0.20"
		}
		ffmpegArgs = append(ffmpegArgs, "-user_agent", userAgent)
		if referer := strings.TrimSpace(channel.HTTPReferer); referer != "" {
			ffmpegArgs = append(ffmpegArgs, "-referer", referer)
		}
		if origin := strings.TrimSpace(channel.HTTPOrigin); origin != "" {
			ffmpegArgs = append(ffmpegArgs, "-headers", "Origin: "+origin+"\r\n")
		}
		ffmpegArgs = append(ffmpegArgs,
			"-i", channel.InputURL,
			"-map", "0",
			"-c", "copy",
			"-max_muxing_queue_size", "2048",
			"-mpegts_flags", "+resend_headers+initial_discontinuity",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "15",
			// discont_start: mark new ffmpeg sessions as discontinuity so players resync cleanly.
			// independent_segments: each segment starts on a keyframe boundary -> no broken P-frames at seam.
			"-hls_flags", "delete_segments+append_list+omit_endlist+discont_start+independent_segments",
			"-hls_delete_threshold", "10",
			"-hls_segment_type", "mpegts",
			"-start_number", fmt.Sprintf("%d", startNumber),
			"-hls_segment_filename", filepath.Join(dir, "seg-%d.ts"),
			playlistPath,
		)
		cmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
		cmd.Stdout = io.Discard
		stderr, _ := cmd.StderrPipe()

		startErr := cmd.Start()
		if startErr != nil {
			message := startErr.Error()
			_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "error", &message)
			m.store.IncrementMetric(context.Background(), channel.ID, "worker_errors", 1)
			logger.Warn("ffmpeg start failed", "error", startErr)
			if firstFailureAt.IsZero() {
				firstFailureAt = time.Now()
			}
			if sourceErrorAfter > 0 && time.Since(firstFailureAt) > sourceErrorAfter {
				_ = m.store.SetSourceStatus(context.Background(), channel.ID, 0, &message)
				logger.Warn("channel auto-disabled: ffmpeg failing too long", "duration", time.Since(firstFailureAt).String())
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff)
			continue
		}

		// ffmpeg started successfully — clear any prior "error" status.
		_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "running", nil)
		backoff = time.Second
		ffmpegStartedAt := time.Now()

		if stderr != nil {
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := stderr.Read(buf)
					if n > 0 {
						logger.Info("ffmpeg", "line", strings.TrimRight(string(buf[:n]), "\n"))
					}
					if err != nil {
						return
					}
				}
			}()
		}

		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		message := "ffmpeg exited"
		if waitErr != nil {
			message = waitErr.Error()
		}
		_ = m.store.SetWorkerStatus(context.Background(), channel.ID, "error", &message)
		m.store.IncrementMetric(context.Background(), channel.ID, "worker_errors", 1)
		logger.Warn("ffmpeg exited", "error", waitErr, "backoff", backoff.String(), "ran", time.Since(ffmpegStartedAt).String())

		// If ffmpeg stayed up for at least 30s it counts as healthy — reset
		// the failure window so transient blips don't trigger auto-disable.
		if time.Since(ffmpegStartedAt) > 30*time.Second {
			firstFailureAt = time.Time{}
		} else if firstFailureAt.IsZero() {
			firstFailureAt = ffmpegStartedAt
		}
		if sourceErrorAfter > 0 && !firstFailureAt.IsZero() && time.Since(firstFailureAt) > sourceErrorAfter {
			_ = m.store.SetSourceStatus(context.Background(), channel.ID, 0, &message)
			logger.Warn("channel auto-disabled: ffmpeg failing too long", "duration", time.Since(firstFailureAt).String())
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff)
	}
}

func clearTransmuxFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
	return nil
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	if next < time.Second {
		return time.Second
	}
	return next
}
