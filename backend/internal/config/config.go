package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	CacheDir          string
	PublicStreamURL   string
	SigningSecret     string
	AdminToken        string
	AdminUsername     string
	AdminPassword     string
	MaxWorkers        int
	UpstreamTimeoutMS int
	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Bucket          string
	R2PublicURL       string
	R2RetentionSecs   int
	LocalRetentionSecs int
	SourceErrorAfterSecs int
	AdminUIOrigins    []string
	PlaybackTokenTTLSecs int
	AllowSignup       bool

	// Node-mode
	Mode            string // "control" (default) or "node"
	ControlPlaneURL string
	NodeAPIKey      string
	NodeHeartbeatSecs int
	NodeConfigPollSecs int
	AgentBinaryDir  string
}

func Load() Config {
	loadDotEnv("../.env")
	loadDotEnv(".env")

	cfg := Config{
		HTTPAddr:          getEnv("HTTP_ADDR", ":3000"),
		DatabaseURL:       firstEnv("DATABASE_URL", "DB_URL"),
		CacheDir:          getEnv("CACHE_DIR", filepath.Join(".", "data", "cache")),
		PublicStreamURL:   strings.TrimRight(getEnv("PUBLIC_STREAM_URL", "http://localhost:3000"), "/"),
		SigningSecret:     getEnv("SIGNING_SECRET", "dev-change-me"),
		AdminToken:        os.Getenv("ADMIN_TOKEN"),
		AdminUsername:     getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
		MaxWorkers:        getEnvInt("MAX_WORKERS", 20),
		UpstreamTimeoutMS: getEnvInt("UPSTREAM_TIMEOUT_MS", 15000),
		R2AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:          os.Getenv("R2_BUCKET"),
		R2PublicURL:       strings.TrimRight(os.Getenv("R2_PUBLIC_URL"), "/"),
		R2RetentionSecs:   getEnvInt("R2_RETENTION_SECONDS", 600),
		LocalRetentionSecs: getEnvInt("LOCAL_RETENTION_SECONDS", 60),
		SourceErrorAfterSecs: getEnvInt("SOURCE_ERROR_AFTER_SECONDS", 300),
		AdminUIOrigins:    splitCSV(firstNonEmpty(os.Getenv("ADMIN_UI_ORIGINS"), os.Getenv("ALLOWED_ORIGINS"))),
		PlaybackTokenTTLSecs: getEnvInt("PLAYBACK_TOKEN_TTL_SECONDS", 900),
		AllowSignup:       getEnvBool("ALLOW_SIGNUP", true),
		Mode:              strings.ToLower(getEnv("MODE", "control")),
		ControlPlaneURL:   strings.TrimRight(getEnv("CONTROL_PLANE_URL", ""), "/"),
		NodeAPIKey:        os.Getenv("NODE_API_KEY"),
		NodeHeartbeatSecs: getEnvInt("NODE_HEARTBEAT_SECONDS", 30),
		NodeConfigPollSecs: getEnvInt("NODE_CONFIG_POLL_SECONDS", 15),
		AgentBinaryDir:    getEnv("AGENT_BINARY_DIR", ""),
	}
	cfg.AgentBinaryDir = resolveAgentBinaryDir(cfg.AgentBinaryDir)
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = cfg.AdminToken
	}
	if cfg.AdminToken == "" {
		cfg.AdminToken = cfg.AdminPassword
	}

	if cfg.DatabaseURL == "" {
		cfg.DatabaseURL = os.Getenv("db")
	}

	return cfg
}

// resolveAgentBinaryDir returns an absolute path to the directory that
// holds the agent binaries served by /agent/<name>. Resolution order:
//   1. AGENT_BINARY_DIR if absolute — use as-is.
//   2. AGENT_BINARY_DIR if relative — resolve against the executable's
//      directory (NOT cwd) so behaviour is deterministic regardless of
//      how the process is started (systemd, supervisor, manual cd).
//   3. If unset, default to "<executable-dir>/bin".
func resolveAgentBinaryDir(raw string) string {
	execDir := ""
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		execDir = filepath.Dir(exe)
	}
	if raw == "" {
		if execDir != "" {
			return filepath.Join(execDir, "bin")
		}
		return "bin"
	}
	if filepath.IsAbs(raw) {
		return raw
	}
	if execDir != "" {
		return filepath.Join(execDir, raw)
	}
	return raw
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var key, value string
		if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
			key, value = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		} else if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
			key, value = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
		key = strings.TrimPrefix(key, "\ufeff")
		if key == "" || value == "" {
			continue
		}
		value = strings.Trim(value, "\"'")
		if strings.EqualFold(key, "db") && os.Getenv("DATABASE_URL") == "" {
			_ = os.Setenv("DATABASE_URL", value)
		}
		if os.Getenv(key) != "" {
			continue
		}
		_ = os.Setenv(key, value)
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
