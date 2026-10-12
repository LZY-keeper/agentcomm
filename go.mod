module agentcomm

go 1.25.0

// 依赖由 `go mod tidy` 自动解析并写入 require / go.sum（构建机需联网）。
// 关键依赖（见 docs/BUILD.md）：
//   github.com/modelcontextprotocol/go-sdk  (官方 MCP Go SDK, v1.x)
//   github.com/a2aproject/a2a-go            (官方 A2A Go SDK)
//   github.com/tetratelabs/wazero                (纯 Go WASM 运行时, 沙箱工具)
//   google.golang.org/grpc / google.golang.org/protobuf
//   github.com/prometheus/client_golang
//   go.opentelemetry.io/otel (+ contrib grpc/otelgrpc)
//   github.com/redis/go-redis/v9

require (
	github.com/prometheus/client_golang v1.24.1
	github.com/redis/go-redis/v9 v9.22.0
	github.com/tetratelabs/wazero v1.12.0
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.46.0
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
	golang.org/x/time v0.12.0
	google.golang.org/grpc v1.84.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.46.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// 本开发机亿赛通透明加密会把 module cache 中的 .c 文件密文化，导致 cgo 构建无法识别
// build tag；此 replace 指向已修复的可写副本，仅影响本机构建，正常环境可删除。
replace golang.org/x/sys => C:/tools/modfix/golang.org/x/sys@v0.47.0
