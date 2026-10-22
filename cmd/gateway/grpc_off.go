//go:build !grpc

package main

import (
	"context"

	"github.com/LZY-keeper/agentcomm/internal/config"
	"github.com/LZY-keeper/agentcomm/internal/orchestrator"
	"github.com/LZY-keeper/agentcomm/internal/registry"
)

// maybeStartGRPC is a no-op without the "grpc" build tag, so the default
// offline gateway builds and runs with no protoc/gRPC dependency. Build with
// -tags grpc (after `make proto`) to enable the gRPC service (see grpc_on.go).
func maybeStartGRPC(_ context.Context, _ config.Config, _ *orchestrator.Orchestrator, _ *registry.Registry) {
}
