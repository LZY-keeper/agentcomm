// Package config centralizes runtime configuration, loaded from environment
// variables with safe defaults so the system runs out-of-the-box (offline mock).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the top-level configuration assembled in cmd/ and injected into
// components (dependency injection). All fields have defaults; env overrides.
type Config struct {
	LogLevel   string `env:"LOG_LEVEL"`   // default info
	LogFormat  string `env:"LOG_FORMAT"`  // default json
	Gateway    GatewayConfig
	LLM        LLMConfig
	Embedding  EmbeddingConfig
	Router     RouterConfig
	Registry   RegistryConfig
	Orchestrator OrchestratorConfig
	Obs        ObsConfig
	Redis      RedisConfig
}

type GatewayConfig struct {
	HTTPAddr   string `env:"GATEWAY_HTTP_ADDR"`   // playground + metrics, default :8080
	GRPCAddr   string `env:"GATEWAY_GRPC_ADDR"`   // default :50051
	A2AAddr    string `env:"GATEWAY_A2A_ADDR"`    // default :5000
}

type LLMConfig struct {
	Provider string `env:"LLM_PROVIDER"`   // mock|qwen|openai|ollama ; default mock
	Model    string `env:"LLM_MODEL"`
	APIKey   string `env:"LLM_API_KEY"`    // also reads QWEN_API_KEY / OPENAI_API_KEY
	BaseURL  string `env:"LLM_BASE_URL"`
}

type EmbeddingConfig struct {
	Provider  string `env:"EMBEDDING_PROVIDER"` // mock|dashscope|openai ; default mock
	Model     string `env:"EMBEDDING_MODEL"`
	APIKey    string `env:"EMBEDDING_API_KEY"`  // also DASHSCOPE_API_KEY
	Dimension int    `env:"EMBEDDING_DIM"`      // default 1024
}

// RouterConfig configures pillar 1 (the federated tool router engine).
type RouterConfig struct {
	MCPServers       []string `env:"MCP_SERVERS"`         // comma-separated upstream MCP server URLs / stdio cmds
	TopK             int      `env:"ROUTER_TOP_K"`        // default 5
	TokenBudget      int      `env:"ROUTER_TOKEN_BUDGET"` // max tokens reserved for tool schemas, default 1500
	SimThreshold     float64  `env:"ROUTER_SIM_THRESHOLD"`// default 0.25
	DenseWeight      float64  `env:"ROUTER_DENSE_W"`      // default 0.6
	SparseWeight     float64  `env:"ROUTER_SPARSE_W"`     // default 0.3
	CapabilityWeight float64  `env:"ROUTER_CAP_W"`        // default 0.1
	EnableCache      bool     `env:"ROUTER_CACHE"`        // default true
	CacheMaxEntries  int      `env:"ROUTER_CACHE_MAX"`    // default 1000
	CacheTTLSeconds  int      `env:"ROUTER_CACHE_TTL"`    // default 3600
}

type RegistryConfig struct {
	Addr            string `env:"REGISTRY_ADDR"`     // default :8500
	HeartbeatSec    int    `env:"REGISTRY_HEARTBEAT"`// default 10
	OfflineTimeoutS int    `env:"REGISTRY_TIMEOUT"`  // default 30
}

type OrchestratorConfig struct {
	IntentByLLM  bool `env:"INTENT_BY_LLM"`   // default true (mock LLM still works offline)
	LBStrategy   string `env:"LB_STRATEGY"`   // round_robin|random|least_conn|weighted_rr|consistent_hash|least_response_time ; default least_response_time
	BreakerFailThreshold int     `env:"BREAKER_FAIL_THRESHOLD"` // default 5
	BreakerOpenSecs      int     `env:"BREAKER_OPEN_SECS"`      // default 30
}

type ObsConfig struct {
	MetricsAddr  string `env:"METRICS_ADDR"`  // default :9090 ; /metrics
	OTLPEndpoint string `env:"OTLP_ENDPOINT"` // optional OTLP/gRPC trace exporter
	TraceStdout  bool   `env:"TRACE_STDOUT"`  // default true (traces to logs, offline friendly)
}

type RedisConfig struct {
	Addr string `env:"REDIS_ADDR"` // default 127.0.0.1:6379 ; empty disables (in-memory fallback)
}

// Load reads configuration from environment, applying defaults.
func Load() Config {
	c := Config{
		LogLevel:  env("LOG_LEVEL", "info"),
		LogFormat: env("LOG_FORMAT", "json"),
		Gateway: GatewayConfig{
			HTTPAddr: env("GATEWAY_HTTP_ADDR", ":8080"),
			GRPCAddr: env("GATEWAY_GRPC_ADDR", ":50051"),
			A2AAddr:  env("GATEWAY_A2A_ADDR", ":5000"),
		},
		LLM: LLMConfig{
			Provider: env("LLM_PROVIDER", "mock"),
			Model:    env("LLM_MODEL", ""),
			APIKey:   firstNonEmpty(os.Getenv("LLM_API_KEY"), os.Getenv("QWEN_API_KEY"), os.Getenv("OPENAI_API_KEY")),
			BaseURL:  env("LLM_BASE_URL", ""),
		},
		Embedding: EmbeddingConfig{
			Provider:  env("EMBEDDING_PROVIDER", "mock"),
			Model:     env("EMBEDDING_MODEL", ""),
			APIKey:    firstNonEmpty(os.Getenv("EMBEDDING_API_KEY"), os.Getenv("DASHSCOPE_API_KEY")),
			Dimension: envInt("EMBEDDING_DIM", 1024),
		},
		Router: RouterConfig{
			MCPServers:       splitCSV(env("MCP_SERVERS", "")),
			TopK:             envInt("ROUTER_TOP_K", 5),
			TokenBudget:      envInt("ROUTER_TOKEN_BUDGET", 1500),
			SimThreshold:     envFloat("ROUTER_SIM_THRESHOLD", 0.25),
			DenseWeight:      envFloat("ROUTER_DENSE_W", 0.6),
			SparseWeight:     envFloat("ROUTER_SPARSE_W", 0.3),
			CapabilityWeight: envFloat("ROUTER_CAP_W", 0.1),
			EnableCache:      envBool("ROUTER_CACHE", true),
			CacheMaxEntries:  envInt("ROUTER_CACHE_MAX", 1000),
			CacheTTLSeconds:  envInt("ROUTER_CACHE_TTL", 3600),
		},
		Registry: RegistryConfig{
			Addr:            env("REGISTRY_ADDR", ":8500"),
			HeartbeatSec:    envInt("REGISTRY_HEARTBEAT", 10),
			OfflineTimeoutS: envInt("REGISTRY_TIMEOUT", 30),
		},
		Orchestrator: OrchestratorConfig{
			IntentByLLM:        envBool("INTENT_BY_LLM", true),
			LBStrategy:         env("LB_STRATEGY", "least_response_time"),
			BreakerFailThreshold: envInt("BREAKER_FAIL_THRESHOLD", 5),
			BreakerOpenSecs:      envInt("BREAKER_OPEN_SECS", 30),
		},
		Obs: ObsConfig{
			MetricsAddr:  env("METRICS_ADDR", ":9090"),
			OTLPEndpoint: env("OTLP_ENDPOINT", ""),
			TraceStdout:  envBool("TRACE_STDOUT", true),
		},
		Redis: RedisConfig{
			Addr: env("REDIS_ADDR", ""),
		},
	}
	if c.LLM.Model == "" {
		switch c.LLM.Provider {
		case "qwen":
			c.LLM.Model = "qwen-plus"
		case "openai", "ollama":
			c.LLM.Model = "gpt-4o-mini"
		default:
			c.LLM.Model = "mock-small"
		}
	}
	if c.Embedding.Model == "" {
		switch c.Embedding.Provider {
		case "dashscope":
			c.Embedding.Model = "text-embedding-v2"
		case "openai":
			c.Embedding.Model = "text-embedding-3-small"
		default:
			c.Embedding.Model = "mock-1024"
		}
	}
	return c
}

// --- helpers ---

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
func envFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// String renders a short summary for startup logging.
func (c Config) String() string {
	return fmt.Sprintf("llm=%s/%s embed=%s/%s router(budget=%d,topk=%d) lb=%s redis=%q",
		c.LLM.Provider, c.LLM.Model, c.Embedding.Provider, c.Embedding.Model,
		c.Router.TokenBudget, c.Router.TopK, c.Orchestrator.LBStrategy, c.Redis.Addr)
}
