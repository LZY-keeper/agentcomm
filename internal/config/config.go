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
	LogLevel     string `env:"LOG_LEVEL"`  // default info
	LogFormat    string `env:"LOG_FORMAT"` // default json
	Gateway      GatewayConfig
	LLM          LLMConfig
	Embedding    EmbeddingConfig
	Router       RouterConfig
	Registry     RegistryConfig
	Orchestrator OrchestratorConfig
	Obs          ObsConfig
	Redis        RedisConfig
	Store        StoreConfig  // task/session persistence (v3)
	Govern       GovernConfig // auth/quota/budget/audit (v3)
	Tools        ToolsConfig  // tool-execution safety limits (v3)
	Eval         EvalConfig   // eval report serving (v3.1)
}

type EvalConfig struct {
	ReportDir string `env:"EVAL_REPORT_DIR"` // default eval/reports; playground reads <dir>/latest/report.json
}

type GatewayConfig struct {
	HTTPAddr string `env:"GATEWAY_HTTP_ADDR"` // playground + metrics, default :8080
	GRPCAddr string `env:"GATEWAY_GRPC_ADDR"` // default :50051
	A2AAddr  string `env:"GATEWAY_A2A_ADDR"`  // default :5000
}

type LLMConfig struct {
	Provider string `env:"LLM_PROVIDER"` // mock|qwen|openai|ollama ; default mock
	Model    string `env:"LLM_MODEL"`
	APIKey   string `env:"LLM_API_KEY"` // also reads QWEN_API_KEY / OPENAI_API_KEY
	BaseURL  string `env:"LLM_BASE_URL"`
}

type EmbeddingConfig struct {
	Provider  string `env:"EMBEDDING_PROVIDER"` // mock|dashscope|openai ; default mock
	Model     string `env:"EMBEDDING_MODEL"`
	APIKey    string `env:"EMBEDDING_API_KEY"` // also DASHSCOPE_API_KEY
	Dimension int    `env:"EMBEDDING_DIM"`     // default 1024
}

// RouterConfig configures pillar 1 (the federated tool router engine).
type RouterConfig struct {
	MCPServers       []string `env:"MCP_SERVERS"`          // comma-separated upstream MCP server URLs / stdio cmds
	TopK             int      `env:"ROUTER_TOP_K"`         // default 5
	TokenBudget      int      `env:"ROUTER_TOKEN_BUDGET"`  // max tokens reserved for tool schemas, default 1500
	SimThreshold     float64  `env:"ROUTER_SIM_THRESHOLD"` // default 0.25
	DenseWeight      float64  `env:"ROUTER_DENSE_W"`       // default 0.6
	SparseWeight     float64  `env:"ROUTER_SPARSE_W"`      // default 0.3
	CapabilityWeight float64  `env:"ROUTER_CAP_W"`         // default 0.1
	EnableCache      bool     `env:"ROUTER_CACHE"`         // default true
	CacheMaxEntries  int      `env:"ROUTER_CACHE_MAX"`     // default 1000
	CacheTTLSeconds  int      `env:"ROUTER_CACHE_TTL"`     // default 3600
}

type RegistryConfig struct {
	Addr            string `env:"REGISTRY_ADDR"`      // default :8500
	HeartbeatSec    int    `env:"REGISTRY_HEARTBEAT"` // default 10
	OfflineTimeoutS int    `env:"REGISTRY_TIMEOUT"`   // default 30
}

type OrchestratorConfig struct {
	IntentByLLM          bool   `env:"INTENT_BY_LLM"`          // default true (mock LLM still works offline)
	LBStrategy           string `env:"LB_STRATEGY"`            // round_robin|random|least_conn|weighted_rr|consistent_hash|least_response_time ; default least_response_time
	BreakerFailThreshold int    `env:"BREAKER_FAIL_THRESHOLD"` // default 5
	BreakerOpenSecs      int    `env:"BREAKER_OPEN_SECS"`      // default 30
}

type ObsConfig struct {
	MetricsAddr  string `env:"METRICS_ADDR"`  // default :9090 ; /metrics
	OTLPEndpoint string `env:"OTLP_ENDPOINT"` // optional OTLP/gRPC trace exporter
	TraceStdout  bool   `env:"TRACE_STDOUT"`  // default true (traces to logs, offline friendly)
}

type RedisConfig struct {
	Addr     string `env:"REDIS_ADDR"`     // empty disables (in-memory fallback)
	Password string `env:"REDIS_PASSWORD"` // optional
	DB       int    `env:"REDIS_DB"`       // optional, default 0
}

// StoreConfig configures task/session persistence (v3 HITL + session memory).
type StoreConfig struct {
	Backend             string `env:"STORE_BACKEND"`       // auto|memory|redis ; auto = redis when REDIS_ADDR set
	TaskTTLSeconds      int    `env:"STORE_TASK_TTL"`      // default 3600
	SessionTTLSeconds   int    `env:"STORE_SESSION_TTL"`   // default 86400
	SessionMaxTurns     int    `env:"STORE_SESSION_TURNS"` // default 20 (messages loaded per session)
	SweepIntervalSecs   int    `env:"STORE_SWEEP"`         // default 30 (memory janitor)
	ApprovalTimeoutSecs int    `env:"APPROVAL_TIMEOUT"`    // default 3600 (input-required park limit)
}

// GovernConfig configures the enterprise governance layer (v3): virtual keys,
// auth, per-key rate limit + token budget, audit. All disabled when no keys
// are configured — the offline demo stays friction-free.
type GovernConfig struct {
	KeysInline        string   `env:"GOV_KEYS"`            // id:sha256:tenant:rps:burst:budget,tool1|tool2,...
	KeysFile          string   `env:"GOV_KEYS_FILE"`       // JSON file of key records
	AllowLoopback     bool     `env:"GOV_ALLOW_LOOPBACK"`  // default true (demo on localhost)
	ExemptPrefixes    []string `env:"GOV_EXEMPT"`          // default /healthz,/metrics,/api/health
	ProtectedPrefixes []string `env:"GOV_PROTECTED"`       // default /api,/a2a,/mcp
	DefaultRPS        float64  `env:"GOV_RPS"`             // default 5
	DefaultBurst      int      `env:"GOV_BURST"`           // default 10
	DefaultBudget     int64    `env:"GOV_BUDGET_TOKENS"`   // default 200000 ; <0 = unlimited
	MaxQueryBytes     int      `env:"GOV_MAX_QUERY_BYTES"` // default 32768 (request body cap)
}

// ToolsConfig bounds tool execution (v3 safety).
type ToolsConfig struct {
	MaxResultBytes   int      `env:"TOOL_MAX_RESULT_BYTES"`  // default 16384 (truncate before LLM)
	MaxFileBytes     int      `env:"TOOL_MAX_FILE_BYTES"`    // default 1048576 (read_file cap)
	ApprovalDefaults []string `env:"TOOLS_REQUIRE_APPROVAL"` // tool names forced to RequireApproval
}

// PublicURL is the externally reachable base URL advertised in the A2A agent
// card (v3). Empty → derived from the local listen address.
type PublicURL string

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
			IntentByLLM:          envBool("INTENT_BY_LLM", true),
			LBStrategy:           env("LB_STRATEGY", "least_response_time"),
			BreakerFailThreshold: envInt("BREAKER_FAIL_THRESHOLD", 5),
			BreakerOpenSecs:      envInt("BREAKER_OPEN_SECS", 30),
		},
		Obs: ObsConfig{
			MetricsAddr:  env("METRICS_ADDR", ":9090"),
			OTLPEndpoint: env("OTLP_ENDPOINT", ""),
			TraceStdout:  envBool("TRACE_STDOUT", true),
		},
		Redis: RedisConfig{
			Addr:     env("REDIS_ADDR", ""),
			Password: env("REDIS_PASSWORD", ""),
			DB:       envInt("REDIS_DB", 0),
		},
		Store: StoreConfig{
			Backend:             env("STORE_BACKEND", "auto"),
			TaskTTLSeconds:      envInt("STORE_TASK_TTL", 3600),
			SessionTTLSeconds:   envInt("STORE_SESSION_TTL", 86400),
			SessionMaxTurns:     envInt("STORE_SESSION_TURNS", 20),
			SweepIntervalSecs:   envInt("STORE_SWEEP", 30),
			ApprovalTimeoutSecs: envInt("APPROVAL_TIMEOUT", 3600),
		},
		Govern: GovernConfig{
			KeysInline:        env("GOV_KEYS", ""),
			KeysFile:          env("GOV_KEYS_FILE", ""),
			AllowLoopback:     envBool("GOV_ALLOW_LOOPBACK", true),
			ExemptPrefixes:    splitCSV(env("GOV_EXEMPT", "/healthz,/metrics,/api/health")),
			ProtectedPrefixes: splitCSV(env("GOV_PROTECTED", "/api,/a2a,/mcp")),
			DefaultRPS:        envFloat("GOV_RPS", 5),
			DefaultBurst:      envInt("GOV_BURST", 10),
			DefaultBudget:     envInt64("GOV_BUDGET_TOKENS", 200000),
			MaxQueryBytes:     envInt("GOV_MAX_QUERY_BYTES", 32768),
		},
		Tools: ToolsConfig{
			MaxResultBytes:   envInt("TOOL_MAX_RESULT_BYTES", 16384),
			MaxFileBytes:     envInt("TOOL_MAX_FILE_BYTES", 1048576),
			ApprovalDefaults: splitCSV(env("TOOLS_REQUIRE_APPROVAL", "")),
		},
		Eval: EvalConfig{
			ReportDir: env("EVAL_REPORT_DIR", "eval/reports"),
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
func envInt64(key string, def int64) int64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
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
	return fmt.Sprintf("llm=%s/%s embed=%s/%s router(budget=%d,topk=%d) lb=%s redis=%q store=%s govern(keys=%t,loopback=%t)",
		c.LLM.Provider, c.LLM.Model, c.Embedding.Provider, c.Embedding.Model,
		c.Router.TokenBudget, c.Router.TopK, c.Orchestrator.LBStrategy, c.Redis.Addr,
		c.Store.Backend, c.KeysConfigured(), c.Govern.AllowLoopback)
}

// KeysConfigured reports whether virtual keys are provisioned (auth on).
func (c Config) KeysConfigured() bool { return c.Govern.KeysInline != "" || c.Govern.KeysFile != "" }
