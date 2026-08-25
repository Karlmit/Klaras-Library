// Package config loads runtime configuration from the environment.
//
// Every setting is read from a KLARAS_-prefixed variable so a container can be
// configured entirely through compose, with no config file to mount.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved configuration for one process.
type Config struct {
	// Core
	DatabaseURL string
	ListenAddr  string
	LogLevel    string
	LogFormat   string // "text" or "json"

	// Filesystem
	LibraryRoot string
	IngestDir   string
	CacheDir    string

	// ExternalURL is the public https base URL this server is reached at.
	// Kobo devices are handed absolute download URLs, so this must be the
	// address the device can actually resolve -- not the container's.
	ExternalURL string

	// Managed tree layout. See filestore for the available placeholders.
	PathTemplateSeries string
	PathTemplatePlain  string
	FileTemplate       string

	// Kobo
	KoboProxyStore bool
	KoboSyncLimit  int

	// Workers
	ConvertWorkers int
	CoverWorkers   int

	// Tuning
	DBMaxConns     int32
	ShutdownGrace  time.Duration
	RequestTimeout time.Duration
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL: env("DATABASE_URL", ""),
		ListenAddr:  env("LISTEN_ADDR", ":8083"),
		LogLevel:    env("LOG_LEVEL", "info"),
		LogFormat:   env("LOG_FORMAT", "text"),

		LibraryRoot: env("LIBRARY_ROOT", ""),
		IngestDir:   env("INGEST_DIR", ""),
		CacheDir:    env("CACHE_DIR", "/cache"),

		ExternalURL: strings.TrimRight(env("EXTERNAL_URL", ""), "/"),

		PathTemplateSeries: env("PATH_TEMPLATE_SERIES", "{author_sort}/{series}/{series_index} - {title}"),
		PathTemplatePlain:  env("PATH_TEMPLATE_PLAIN", "{author_sort}/{title}"),
		FileTemplate:       env("FILE_TEMPLATE", "{title} - {author_sort}"),

		KoboProxyStore: envBool("KOBO_PROXY_STORE", false),
		KoboSyncLimit:  envInt("KOBO_SYNC_LIMIT", 100),

		ConvertWorkers: envInt("CONVERT_WORKERS", 2),
		CoverWorkers:   envInt("COVER_WORKERS", 4),

		DBMaxConns:     int32(envInt("DB_MAX_CONNS", 16)),
		ShutdownGrace:  envDuration("SHUTDOWN_GRACE", 20*time.Second),
		RequestTimeout: envDuration("REQUEST_TIMEOUT", 60*time.Second),
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("KLARAS_DATABASE_URL is required")
	}
	if c.LibraryRoot == "" {
		return fmt.Errorf("KLARAS_LIBRARY_ROOT is required")
	}
	abs, err := filepath.Abs(c.LibraryRoot)
	if err != nil {
		return fmt.Errorf("KLARAS_LIBRARY_ROOT: %w", err)
	}
	c.LibraryRoot = abs

	if c.ExternalURL != "" {
		u, err := url.Parse(c.ExternalURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("KLARAS_EXTERNAL_URL must be an absolute URL, got %q", c.ExternalURL)
		}
		// Kobo firmware refuses to download over plain http when the store URL
		// it was given is https, and silently fails. Catch it at boot instead.
		if u.Scheme != "https" && !isLoopback(u.Hostname()) {
			return fmt.Errorf("KLARAS_EXTERNAL_URL must be https (Kobo download links fail over http), got %q", c.ExternalURL)
		}
	}
	if c.KoboSyncLimit < 1 || c.KoboSyncLimit > 1000 {
		return fmt.Errorf("KLARAS_KOBO_SYNC_LIMIT must be 1..1000, got %d", c.KoboSyncLimit)
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func env(key, def string) string {
	if v, ok := os.LookupEnv("KLARAS_" + key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := env(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := env(key, ""); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := env(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
