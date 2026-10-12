// Command registry runs the A2A agent discovery service (HTTP).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agentcomm/internal/config"
	"agentcomm/internal/logx"
	"agentcomm/internal/registry"
)

func main() {
	cfg := config.Load()
	logx.Configure(cfg.LogLevel, cfg.LogFormat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := registry.New(time.Duration(cfg.Registry.OfflineTimeoutS) * time.Second)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	logx.Info(ctx, "registry listening", "addr", cfg.Registry.Addr)
	if err := reg.Serve(ctx, cfg.Registry.Addr); err != nil && ctx.Err() == nil {
		logx.Error(ctx, "registry serve", "err", err)
		os.Exit(1)
	}
}
