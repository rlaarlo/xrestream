package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"restream/backend/internal/config"
	"restream/backend/internal/httpapi"
	"restream/backend/internal/nodeagent"
	"restream/backend/internal/relay"
	"restream/backend/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Mode == "node" {
		agent := nodeagent.New(cfg, logger)
		if err := agent.Run(ctx); err != nil {
			logger.Error("node agent failed", "error", err)
			os.Exit(1)
		}
		return
	}

	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := database.Migrate(ctx); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}

	var r2Client *relay.R2Client
	if cfg.R2AccountID != "" && cfg.R2Bucket != "" && cfg.R2AccessKeyID != "" && cfg.R2SecretAccessKey != "" {
		r2Client = relay.NewR2Client(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Bucket, cfg.R2PublicURL)
		logger.Info("r2 sync enabled", "bucket", cfg.R2Bucket)
	}

	relayManager := relay.NewManager(database, cfg, logger, r2Client)
	if err := relayManager.StartActiveWorkers(ctx); err != nil {
		logger.Warn("active worker bootstrap failed", "error", err)
	}

	handler := httpapi.NewServer(cfg, database, relayManager, logger)
	if err := handler.EnsureSeedData(); err != nil {
		logger.Error("seed data failed", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("api server started", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api server failed", "error", err)
			stop()
		}
	}()

	// Periodically mark nodes whose heartbeat is older than 3x the expected
	// interval as offline so the dashboard reflects reality after a node
	// crash/network partition.
	go func() {
		interval := time.Duration(cfg.NodeHeartbeatSecs) * time.Second
		if interval <= 0 {
			interval = 30 * time.Second
		}
		cutoff := interval * 3
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				database.MarkStaleNodesOffline(ctx, cutoff)
			}
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown requested")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	relayManager.StopAll()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api server shutdown failed", "error", err)
	}
}
