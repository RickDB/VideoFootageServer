package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	ListenAddr, DatabasePath, ExportRoot, EncryptionKey, LogLevel, FFmpegPath string
	AdminUsername, AdminPassword                                              string
	RequestTimeout                                                            time.Duration
	SessionTTL                                                                time.Duration
	MaxConcurrentExports                                                      int
	AllowPrivateDestinations, PreserveRaw, SecureCookies                      bool
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:               env("APP_LISTEN_ADDR", ":8080"),
		DatabasePath:             env("APP_DATABASE_PATH", "./data/hikvision.db"),
		ExportRoot:               env("APP_EXPORT_ROOT", "./exports"),
		EncryptionKey:            os.Getenv("APP_ENCRYPTION_KEY"),
		LogLevel:                 env("APP_LOG_LEVEL", "info"),
		FFmpegPath:               env("APP_FFMPEG_PATH", "ffmpeg"),
		AdminUsername:            env("APP_ADMIN_USERNAME", "admin"),
		AdminPassword:            env("APP_ADMIN_PASSWORD", "change-me-now"),
		RequestTimeout:           duration("APP_REQUEST_TIMEOUT", 30*time.Second),
		SessionTTL:               duration("APP_SESSION_TTL", 12*time.Hour),
		MaxConcurrentExports:     integer("APP_MAX_CONCURRENT_EXPORTS", 4),
		AllowPrivateDestinations: boolean("APP_ALLOW_CUSTOM_DESTINATIONS", true),
		PreserveRaw:              boolean("APP_PRESERVE_RAW", true),
		SecureCookies:            boolean("APP_SECURE_COOKIES", false),
	}
	if c.MaxConcurrentExports < 1 {
		return c, fmt.Errorf("APP_MAX_CONCURRENT_EXPORTS must be positive")
	}
	if c.AdminUsername == "" || len(c.AdminPassword) < 12 {
		return c, fmt.Errorf("APP_ADMIN_USERNAME is required and APP_ADMIN_PASSWORD must be at least 12 characters")
	}
	if c.SessionTTL < 5*time.Minute {
		return c, fmt.Errorf("APP_SESSION_TTL must be at least 5m")
	}
	for _, path := range []string{filepath.Dir(c.DatabasePath), c.ExportRoot} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			return c, err
		}
	}
	return c, nil
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func integer(k string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(k))
	if err == nil {
		return v
	}
	return fallback
}
func boolean(k string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(k))
	if err == nil {
		return v
	}
	return fallback
}
func duration(k string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(k))
	if err == nil {
		return v
	}
	return fallback
}
