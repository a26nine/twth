package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"twth/rpcproxy/internal/app"
	"twth/rpcproxy/internal/config"
)

func main() {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler := app.NewHandler(http.NotFoundHandler(), logger)
	logger.Info("starting HTTP server", "address", cfg.ListenAddr, "upstream", cfg.UpstreamURL.Redacted())
	if err := app.Run(ctx, cfg, handler, logger); err != nil {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
