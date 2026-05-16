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
	}
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
