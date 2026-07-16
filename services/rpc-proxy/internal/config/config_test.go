package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(lookup(nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got, want := cfg.ListenAddr, ":8080"; got != want {
		t.Errorf("ListenAddr = %q, want %q", got, want)
	}
	if got, want := cfg.UpstreamURL.String(), "https://polygon.drpc.org"; got != want {
		t.Errorf("UpstreamURL = %q, want %q", got, want)
	}
	if got, want := cfg.MaxRequestBytes, int64(10*1024*1024); got != want {
		t.Errorf("MaxRequestBytes = %d, want %d", got, want)
	}
	if got, want := cfg.UpstreamResponseHeaderTimeout, 30*time.Second; got != want {
		t.Errorf("UpstreamResponseHeaderTimeout = %s, want %s", got, want)
	}
	if got, want := cfg.ShutdownTimeout, 15*time.Second; got != want {
		t.Errorf("ShutdownTimeout = %s, want %s", got, want)
	}
	if got, want := cfg.LogLevel, slog.LevelInfo; got != want {
		t.Errorf("LogLevel = %s, want %s", got, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(lookup(map[string]string{
		"LISTEN_ADDR":                      "127.0.0.1:9090",
		"UPSTREAM_URL":                     "https://example.com/base",
		"MAX_REQUEST_BYTES":                "2048",
		"UPSTREAM_RESPONSE_HEADER_TIMEOUT": "45s",
		"SHUTDOWN_TIMEOUT":                 "5s",
		"LOG_LEVEL":                        "debug",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ListenAddr != "127.0.0.1:9090" ||
		cfg.UpstreamURL.String() != "https://example.com/base" ||
		cfg.MaxRequestBytes != 2048 ||
		cfg.UpstreamResponseHeaderTimeout != 45*time.Second ||
		cfg.ShutdownTimeout != 5*time.Second ||
		cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("Load() returned unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "empty listen address", values: map[string]string{"LISTEN_ADDR": ""}},
		{name: "invalid upstream", values: map[string]string{"UPSTREAM_URL": "://bad"}},
		{name: "unsupported upstream scheme", values: map[string]string{"UPSTREAM_URL": "ftp://example.com"}},
		{name: "missing upstream host", values: map[string]string{"UPSTREAM_URL": "https:///rpc"}},
		{name: "upstream query", values: map[string]string{"UPSTREAM_URL": "https://example.com?key=value"}},
		{name: "upstream fragment", values: map[string]string{"UPSTREAM_URL": "https://example.com/#fragment"}},
		{name: "empty upstream fragment", values: map[string]string{"UPSTREAM_URL": "https://example.com/#"}},
		{name: "non-numeric byte limit", values: map[string]string{"MAX_REQUEST_BYTES": "large"}},
		{name: "zero byte limit", values: map[string]string{"MAX_REQUEST_BYTES": "0"}},
		{name: "invalid response timeout", values: map[string]string{"UPSTREAM_RESPONSE_HEADER_TIMEOUT": "soon"}},
		{name: "zero response timeout", values: map[string]string{"UPSTREAM_RESPONSE_HEADER_TIMEOUT": "0s"}},
		{name: "invalid shutdown timeout", values: map[string]string{"SHUTDOWN_TIMEOUT": "later"}},
		{name: "zero shutdown timeout", values: map[string]string{"SHUTDOWN_TIMEOUT": "0s"}},
		{name: "invalid log level", values: map[string]string{"LOG_LEVEL": "trace"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(lookup(tt.values)); err == nil {
				t.Fatal("Load() error = nil, want non-nil")
			}
		})
	}
}

func TestLoadSanitizesInvalidUpstreamError(t *testing.T) {
	const secret = "super-secret"
	_, err := Load(lookup(map[string]string{
		"UPSTREAM_URL": "https://user:" + secret + "@example.com/%zz",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want non-nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error contains upstream credential: %q", err)
	}
}
