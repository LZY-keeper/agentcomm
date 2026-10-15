// Package obs wires observability: Prometheus metrics (including business
// metrics on tool-selection token savings and retrieval quality) and
// OpenTelemetry tracing. These are what turn "it works" into "it's
// provably cheap, fast, and debuggable" — a headline interview signal.
package obs

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"net/http"
)

// Metrics holds the project's Prometheus collectors. Use the methods to
// instrument code; do not touch the collectors directly.
type Metrics struct {
	reg *prometheus.Registry

	RouterTokens       *prometheus.CounterVec // phase=full|packed ; diff = tokens saved
	RouterRetrievalSec *prometheus.HistogramVec
	RouterPackedTools  *prometheus.HistogramVec // tools actually injected
	LLMTokens          *prometheus.CounterVec   // provider, kind=prompt|completion
	LLMReqSec          *prometheus.HistogramVec
	ToolCalls          *prometheus.CounterVec // tool, server, result=ok|err
	RouteDecisions     *prometheus.CounterVec // agent, intent
	BreakerState       *prometheus.GaugeVec   // agent, state=closed|open|halfopen
	CostTotal          *prometheus.CounterVec // provider ; unit: CNY *1000 (milli)
}

// NewMetrics registers all collectors on a fresh registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	m := &Metrics{reg: reg}
	m.RouterTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "agentcomm", Name: "router_tokens_total",
		Help: "Tokens reserved for tool schemas, by phase (full=naive inject, packed=budget-packed).",
	}, []string{"phase"})
	m.RouterRetrievalSec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "agentcomm", Name: "router_retrieval_seconds",
		Help:    "Tool retrieval latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"mode"})
	m.RouterPackedTools = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "agentcomm", Name: "router_packed_tools",
		Help:    "Number of tools injected after packing.",
		Buckets: []float64{0, 1, 2, 3, 5, 8, 13, 21, 34},
	}, []string{})
	m.LLMTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "agentcomm", Name: "llm_tokens_total",
		Help: "LLM tokens consumed.",
	}, []string{"provider", "kind"})
	m.LLMReqSec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "agentcomm", Name: "llm_request_seconds",
		Help:    "LLM request latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider"})
	m.ToolCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "agentcomm", Name: "tool_calls_total",
		Help: "Tool invocations.",
	}, []string{"tool", "server", "result"})
	m.RouteDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "agentcomm", Name: "orchestrator_route_total",
		Help: "Agent routing decisions.",
	}, []string{"agent", "intent"})
	m.BreakerState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "agentcomm", Name: "breaker_state",
		Help: "Circuit breaker state (0 closed, 1 half-open, 2 open).",
	}, []string{"agent", "state"})
	m.CostTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "agentcomm", Name: "cost_milli_total",
		Help: "Accumulated cost in milli-currency units.",
	}, []string{"provider"})
	reg.MustRegister(
		m.RouterTokens, m.RouterRetrievalSec, m.RouterPackedTools,
		m.LLMTokens, m.LLMReqSec, m.ToolCalls, m.RouteDecisions, m.BreakerState, m.CostTotal,
	)
	return m
}

// Registry returns the underlying Prometheus registry.
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// HTTPHandler returns the /metrics HTTP handler.
func (m *Metrics) HTTPHandler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// --- Tracing ---

// InitTracer builds a tracer provider. If otlpEndpoint is set it exports via
// OTLP/gRPC; else if traceStdout it prints spans to stdout; else no-op.
// Returns a shutdown function the caller defers.
func InitTracer(ctx context.Context, svcName, otlpEndpoint string, traceStdout bool) (trace.Tracer, func(context.Context) error, error) {
	var (
		exp sdktrace.SpanExporter
		err error
	)
	switch {
	case otlpEndpoint != "":
		// Lazy import keeps the no-network default path dependency-light.
		exp, err = newOTLPExporter(ctx, otlpEndpoint)
		if err != nil {
			return nil, nil, err
		}
	case traceStdout:
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, nil, err
		}
	default:
		exp = noopExporter{}
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL, semconv.ServiceName(svcName)))
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Tracer("agentcomm"), tp.Shutdown, nil
}

// noopExporter is a no-op span exporter for when tracing is disabled.
type noopExporter struct{}

func (noopExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (noopExporter) Shutdown(context.Context) error                             { return nil }

// newOTLPExporter is implemented in otlp.go (lazy, only compiled when used).
// It is referenced here to keep InitTracer linear; see otlp.go for the body.
