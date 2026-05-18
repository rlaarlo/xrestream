package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"restream/backend/internal/config"
	"restream/backend/internal/relay"
	"restream/backend/internal/store"
)

type ConfigResponse struct {
	Node struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Host string `json:"host"`
	} `json:"node"`
	SigningSecret   string          `json:"signingSecret"`
	PublicStreamURL string          `json:"publicStreamURL"`
	R2              *store.R2Config `json:"r2,omitempty"`
	Channels        []store.Channel `json:"channels"`
}

type Agent struct {
	cfg    config.Config
	logger *slog.Logger
	client *http.Client

	store *memStore

	mu           sync.Mutex
	relay        *relay.Manager
	httpServer   *http.Server
	signingReady bool
	r2Sig        string // current R2 config signature, to detect changes
}

func New(cfg config.Config, logger *slog.Logger) *Agent {
	return &Agent{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{Timeout: 20 * time.Second},
		store:  newMemStore(),
	}
}

func (a *Agent) Run(ctx context.Context) error {
	if a.cfg.ControlPlaneURL == "" {
		return fmt.Errorf("CONTROL_PLANE_URL is required in node mode")
	}
	if a.cfg.NodeAPIKey == "" {
		return fmt.Errorf("NODE_API_KEY is required in node mode")
	}
	a.logger.Info("node agent starting",
		"controlPlane", a.cfg.ControlPlaneURL,
		"httpAddr", a.cfg.HTTPAddr,
		"heartbeatSecs", a.cfg.NodeHeartbeatSecs,
		"pollSecs", a.cfg.NodeConfigPollSecs)

	// Initial heartbeat + config sync (best-effort).
	if err := a.heartbeat(ctx); err != nil {
		a.logger.Warn("initial heartbeat failed", "error", err)
	}
	if err := a.syncConfig(ctx); err != nil {
		a.logger.Warn("initial config sync failed", "error", err)
	}

	heartbeatTicker := time.NewTicker(time.Duration(a.cfg.NodeHeartbeatSecs) * time.Second)
	defer heartbeatTicker.Stop()
	pollTicker := time.NewTicker(time.Duration(a.cfg.NodeConfigPollSecs) * time.Second)
	defer pollTicker.Stop()
	reportTicker := time.NewTicker(10 * time.Second)
	defer reportTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("node agent stopping")
			a.shutdown()
			return nil
		case <-heartbeatTicker.C:
			if err := a.heartbeat(ctx); err != nil {
				a.logger.Warn("heartbeat failed", "error", err)
			}
		case <-pollTicker.C:
			if err := a.syncConfig(ctx); err != nil {
				a.logger.Warn("config sync failed", "error", err)
			}
		case <-reportTicker.C:
			if err := a.flushReports(ctx); err != nil {
				a.logger.Warn("report flush failed", "error", err)
			}
		}
	}
}

func (a *Agent) shutdown() {
	a.mu.Lock()
	rm := a.relay
	srv := a.httpServer
	a.mu.Unlock()
	if rm != nil {
		rm.StopAll()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.flushReports(flushCtx)
}

func (a *Agent) heartbeat(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlPlaneURL+"/node/heartbeat", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Node-Key", a.cfg.NodeAPIKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("heartbeat status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (a *Agent) syncConfig(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.ControlPlaneURL+"/node/config", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Node-Key", a.cfg.NodeAPIKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("config status %d: %s", resp.StatusCode, string(body))
	}
	var cfg ConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return err
	}

	// First-time bootstrap: install the signing secret from the control
	// plane and bring up the local relay manager + HTTP server.
	if !a.signingReady {
		if cfg.SigningSecret == "" {
			return errors.New("control plane returned empty signingSecret; cannot start node relay")
		}
		a.cfg.SigningSecret = cfg.SigningSecret
		if cfg.PublicStreamURL != "" {
			a.cfg.PublicStreamURL = cfg.PublicStreamURL
		}
		if err := a.startRelayAndHTTP(ctx, cfg.R2); err != nil {
			return fmt.Errorf("start relay: %w", err)
		}
		a.r2Sig = r2Signature(cfg.R2)
		a.signingReady = true
	} else if sig := r2Signature(cfg.R2); sig != a.r2Sig {
		// R2 config changed at runtime: rebuild the relay manager's R2
		// client. Workers keep running; new uploads use the new client.
		a.mu.Lock()
		if a.relay != nil {
			a.relay.SetR2Client(buildR2Client(cfg.R2))
		}
		a.mu.Unlock()
		a.r2Sig = sig
		a.logger.Info("r2 config updated from control plane")
	}

	added, removed := a.store.Replace(cfg.Channels)

	a.mu.Lock()
	rm := a.relay
	a.mu.Unlock()
	if rm == nil {
		return nil
	}
	for _, ch := range removed {
		a.logger.Info("channel removed", "id", ch.ID, "slug", ch.Slug)
		rm.StopWorker(ch.ID)
		_ = rm.PurgeChannel(ch.Slug)
	}
	for _, ch := range added {
		a.logger.Info("channel assigned", "id", ch.ID, "slug", ch.Slug, "mode", ch.Mode, "status", ch.Status)
		if ch.Status == "active" && (ch.Mode == "ingest" || ch.Mode == "transmux") {
			rm.StartWorker(ctx, ch)
		}
	}
	a.logger.Info("config synced", "channelCount", len(cfg.Channels), "added", len(added), "removed", len(removed))
	return nil
}

func (a *Agent) startRelayAndHTTP(ctx context.Context, r2cfg *store.R2Config) error {
	rm := relay.NewManager(a.store, a.cfg, a.logger, buildR2Client(r2cfg))
	a.mu.Lock()
	a.relay = rm
	a.mu.Unlock()
	// StartActiveWorkers starts the disk-cache cleanup loop. Channels are
	// not yet populated; subsequent reconcile cycles call StartWorker.
	if err := rm.StartActiveWorkers(ctx); err != nil {
		a.logger.Warn("active worker bootstrap failed", "error", err)
	}

	handler := newHTTPServer(rm, a.store, a.logger)
	addr := a.cfg.HTTPAddr
	if addr == "" {
		addr = ":3000"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.httpServer = srv
	a.mu.Unlock()
	go func() {
		a.logger.Info("node http server listening", "addr", addr)
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error("node http server failed", "error", err)
		}
	}()
	return nil
}

func (a *Agent) flushReports(ctx context.Context) error {
	statuses, metrics := a.store.DrainReports()
	if len(statuses) == 0 && len(metrics) == 0 {
		return nil
	}
	type statusEntry struct {
		ChannelID    string  `json:"channelId"`
		WorkerStatus string  `json:"workerStatus,omitempty"`
		LastError    *string `json:"lastError,omitempty"`
		SourceStatus *int    `json:"sourceStatus,omitempty"`
	}
	type metricEntry struct {
		ChannelID string `json:"channelId"`
		Field     string `json:"field"`
		Amount    int64  `json:"amount"`
	}
	payload := struct {
		Statuses []statusEntry `json:"statuses"`
		Metrics  []metricEntry `json:"metrics"`
	}{}
	for id, rep := range statuses {
		payload.Statuses = append(payload.Statuses, statusEntry{
			ChannelID:    id,
			WorkerStatus: rep.WorkerStatus,
			LastError:    rep.LastError,
			SourceStatus: rep.SourceStatus,
		})
	}
	for key, amount := range metrics {
		payload.Metrics = append(payload.Metrics, metricEntry{
			ChannelID: key.ChannelID,
			Field:     key.Field,
			Amount:    amount,
		})
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlPlaneURL+"/node/report", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("X-Node-Key", a.cfg.NodeAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("report status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// buildR2Client constructs a relay.R2Client from a control-plane-supplied R2
// config. Returns nil when any required field is missing, so the relay
// manager simply skips R2 sync and serves segments from disk cache only.
func buildR2Client(c *store.R2Config) *relay.R2Client {
	if c == nil {
		return nil
	}
	if c.AccountID == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" || c.Bucket == "" {
		return nil
	}
	return relay.NewR2Client(c.AccountID, c.AccessKeyID, c.SecretAccessKey, c.Bucket, c.PublicURL)
}

// r2Signature returns a stable string fingerprint of the R2 config so the
// agent can detect changes between config polls without storing credentials
// in plaintext for comparison purposes.
func r2Signature(c *store.R2Config) string {
	if c == nil {
		return ""
	}
	return c.AccountID + "|" + c.Bucket + "|" + c.AccessKeyID + "|" + c.PublicURL + "|" + c.SecretAccessKey
}