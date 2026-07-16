package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr      = ":8080"
	defaultUpstreamURL     = "https://polygon.drpc.org"
	defaultMaxRequestBytes = int64(10 * 1024 * 1024)
	defaultResponseTimeout = 30 * time.Second
	defaultShutdownTimeout = 15 * time.Second
)

type Config struct {
	ListenAddr                    string
	UpstreamURL                   *url.URL
	MaxRequestBytes               int64
	UpstreamResponseHeaderTimeout time.Duration
	ShutdownTimeout               time.Duration
	LogLevel                      slog.Level
}

func Load(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("environment lookup function is nil")
	}

	listenAddr := valueOrDefault(lookup, "LISTEN_ADDR", defaultListenAddr)
	if strings.TrimSpace(listenAddr) == "" {
		return Config{}, fmt.Errorf("LISTEN_ADDR must not be empty")
	}

	upstreamValue := valueOrDefault(lookup, "UPSTREAM_URL", defaultUpstreamURL)
	upstream, err := url.Parse(upstreamValue)
	if err != nil {
		return Config{}, fmt.Errorf("parse UPSTREAM_URL: invalid URL")
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return Config{}, fmt.Errorf("UPSTREAM_URL scheme must be http or https")
	}
	if upstream.Host == "" {
		return Config{}, fmt.Errorf("UPSTREAM_URL must include a host")
	}
	if upstream.RawQuery != "" || upstream.ForceQuery {
		return Config{}, fmt.Errorf("UPSTREAM_URL must not include a query")
	}
	if strings.Contains(upstreamValue, "#") {
		return Config{}, fmt.Errorf("UPSTREAM_URL must not include a fragment")
	}

	maxRequestBytes, err := parsePositiveInt64(lookup, "MAX_REQUEST_BYTES", defaultMaxRequestBytes)
	if err != nil {
		return Config{}, err
	}
	responseTimeout, err := parsePositiveDuration(lookup, "UPSTREAM_RESPONSE_HEADER_TIMEOUT", defaultResponseTimeout)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := parsePositiveDuration(lookup, "SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(valueOrDefault(lookup, "LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("parse LOG_LEVEL: %w", err)
	}

	return Config{
		ListenAddr:                    listenAddr,
		UpstreamURL:                   upstream,
		MaxRequestBytes:               maxRequestBytes,
		UpstreamResponseHeaderTimeout: responseTimeout,
		ShutdownTimeout:               shutdownTimeout,
		LogLevel:                      level,
	}, nil
}

func valueOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

func parsePositiveInt64(lookup func(string) (string, bool), key string, fallback int64) (int64, error) {
	value, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}

func parsePositiveDuration(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}
