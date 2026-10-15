//go:build grpc

package main

import (
	"context"
	"net"

	"agentcomm/internal/config"
	"agentcomm/internal/logx"
	"agentcomm/internal/orchestrator"
	"agentcomm/internal/registry"
	"agentcomm/internal/rpc"

	"google.golang.org/grpc"
)

// maybeStartGRPC starts the gRPC AI-query + inter-agent services when the
// "grpc" build tag is set. Requires generated stubs (run `make proto` first).
func maybeStartGRPC(ctx context.Context, cfg config.Config, orch *orchestrator.Orchestrator, reg *registry.Registry) {
	lis, err := net.Listen("tcp", cfg.Gateway.GRPCAddr)
	if err != nil {
		logx.Error(ctx, "grpc listen", "err", err)
		return
	}
	g := grpc.NewServer()
	rpc.New(orch, reg).Register(g)
	go func() {
		logx.Info(ctx, "gRPC listening (4 modes)", "addr", cfg.Gateway.GRPCAddr)
		if err := g.Serve(lis); err != nil {
			logx.Error(ctx, "grpc serve", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		g.GracefulStop()
	}()
}
