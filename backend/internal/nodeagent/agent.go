package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"restream/backend/internal/config"
	"restream/backend/internal/store"
)

type ConfigResponse struct {
	Node struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Host string `json:"host"`
	} `json:"node"`
	R2       *store.R2Config  `json:"r2,omitempty"`
	Channels []store.Channel  `json:"channels"`
}

type Agent struct {
	cfg     config.Config
	logger  *slog.Logger
	client  *http.Client
	current map[string]store.Channel // channelID -> channel
}

func New(cfg config.Config, logger *slog.Logger) *Agent {
	return &Agent{
		cfg:     cfg,
		logger:  logger,
		client:  &http.Client{Timeout: 20 * time.Second},
		current: map[string]store.Channel{},
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
		"heartbeatSecs", a.cfg.NodeHeartbeatSecs,
		"pollSecs", a.cfg.NodeConfigPollSecs)

	// Initial heartbeat + config sync (best-effort)
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

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("node agent stopping")
			return nil
		case <-heartbeatTicker.C:
			if err := a.heartbeat(ctx); err != nil {
				a.logger.Warn("heartbeat failed", "error", err)
			}
		case <-pollTicker.C:
			if err := a.syncConfig(ctx); err != nil {
				a.logger.Warn("config sync failed", "error", err)
			}
		}
	}
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
	a.reconcile(cfg.Channels)
	return nil
}

// reconcile diffs desired channel set against current and logs additions/removals.
// Tahap 2: ganti log dengan StartWorker/StopWorker.
func (a *Agent) reconcile(desired []store.Channel) {
	next := map[string]store.Channel{}
	for _, ch := range desired {
		next[ch.ID] = ch
	}
	for id, ch := range next {
		if _, existed := a.current[id]; !existed {
			a.logger.Info("channel assigned", "id", id, "slug", ch.Slug, "mode", ch.Mode, "status", ch.Status)
		}
	}
	for id, ch := range a.current {
		if _, stillThere := next[id]; !stillThere {
			a.logger.Info("channel removed", "id", id, "slug", ch.Slug)
		}
	}
	a.current = next
	a.logger.Info("config synced", "channelCount", len(next))
}
