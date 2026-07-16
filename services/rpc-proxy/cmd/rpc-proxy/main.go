package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"twth/rpcproxy/internal/app"
	"twth/rpcproxy/internal/buildinfo"
	"twth/rpcproxy/internal/config"
	"twth/rpcproxy/internal/proxy"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Fprintln(os.Stdout, buildinfo.Current())
		return
	}

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	transport := proxy.NewTransport(cfg.UpstreamResponseHeaderTimeout)
	defer transport.CloseIdleConnections()
	proxyHandler := proxy.NewHandler(proxy.Options{
		Upstream:        cfg.UpstreamURL,
		Transport:       transport,
		MaxRequestBytes: cfg.MaxRequestBytes,
		Logger:          logger,
	})
	handler := app.NewHandler(proxyHandler, logger, buildinfo.Current())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("starting HTTP server", "address", cfg.ListenAddr, "upstream", cfg.UpstreamURL.Redacted())
	if err := app.Run(ctx, cfg, handler, logger); err != nil {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
