package obs

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// newOTLPExporter builds an OTLP/gRPC trace exporter for a remote collector
// (e.g. Jaeger, Tempo, Alloy). Only invoked when OTLP_ENDPOINT is configured.
func newOTLPExporter(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
}
