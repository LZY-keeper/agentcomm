# 编译与运行指南

`agentcomm` 用 Go 1.22+ 实现，纯 Go（无 cgo），跨平台单二进制。**默认离线可跑**（内置确定性 mock LLM/embedding，无需任何 API key）。

> 约束说明：本项目在开发机（无 Go、无外网）上无法就地编译验证；请在装有 Go 1.22+ 且能访问 `proxy.golang.org` 的机器上构建。`go mod tidy` 会自动解析依赖并生成 `go.sum`。

## 0. 环境要求

| 依赖 | 版本 | 何时需要 |
|------|------|----------|
| Go | 1.22+ | 必需 |
| protoc + protoc-gen-go + protoc-gen-go-grpc | 任意较新 | 仅 gRPC（`make grpc`） |
| TinyGo（WASI） | 0.30+ | 仅自建 WASM 工具（可选） |
| Docker / docker compose | — | 仅容器部署（可选） |
| Redis | — | 可选（默认内存态） |

首次：
```bash
cd go-v2
go mod tidy          # 拉取依赖（wazero、google.golang.org/grpc、OpenTelemetry、Prometheus 等；MCP/A2A 按官方规范手写实现，无外部 SDK）
make test            # 运行单元/集成测试（应全绿）
```

## 1. 方式 A：离线一键 demo（推荐先跑）

```bash
make build           # 编译 gateway / agent-math / mcpserver / registry（无需 protoc）
make run             # 启动超级 Agent 网关（默认 mock provider）
```

打开 **http://localhost:8080**，输入「计算 123+456」：
- 流式逐 token 输出 `579`；
- 右侧「路由决策」面板展示：意图 `math`、命中 `math-1`、混合检索命中的工具及 fused/dense/bm25/cap 分数、**token 装箱节省 %**、工具调用、延时。

其它端点：
- 健康检查：`GET /healthz`
- 指标：`GET /metrics`（Prometheus）
- A2A Agent Card：`GET /.well-known/agent-card.json`，RPC：`POST /a2a/`
- 路由引擎量化：`make bench-router`（打印每查询的 token 节省 % 表）

## 2. 方式 B：接真实 LLM / Embedding

```bash
export LLM_PROVIDER=qwen          # 或 openai / ollama
export QWEN_API_KEY=sk-xxx        # QWEN 用 DashScope 兼容模式
export EMBEDDING_PROVIDER=dashscope
export DASHSCOPE_API_KEY=sk-xxx
make run
```
同一 Playground 现在走真实 Qwen SSE 流式 + DashScope 向量。OpenAI/DeepSeek/Ollama 同理（`LLM_PROVIDER=openai OPENAI_API_KEY=...`，或 `LLM_PROVIDER=ollama LLM_BASE_URL=http://localhost:11434/v1`）。

## 3. 方式 C：gRPC 四模式（需 protoc）

```bash
make grpc            # = make proto && 构建 gateway-grpc（-tags grpc）+ cli
./bin/gateway-grpc & # 在 :50051 暴露 gRPC（AIQueryService 四模式 + AgentCommunicationService）
./bin/cli -addr localhost:50051 -q "计算 7*6"
# 演示：unary Query + server-stream QueryStream（真 token 流）
```
`AgentCommunicationService` 的 `RealTimeCommunication`（bidi）做真正的跨客户端消息路由（broker），不再是回声桩。

## 4. 方式 D：分布式多 Agent（A2A + Registry）

```bash
DISTRIBUTED=1 make run     # 启动 registry(:8500) + 独立 math-agent(:5001) + gateway(:8080)
```
独立 agent 会向 registry 注册并心跳；网关可经 A2A 调用远端 agent（`orchestrator.NewA2AAgent` 适配器）。

## 5. 方式 E：MCP（作为子进程 / 外部客户端）

```bash
# MCP server（stdio 传输，可被外部 MCP 客户端或网关联邦调用）
./bin/mcpserver
# 网关侧：把外部 MCP server 加入联邦（环境变量）
MCP_SERVERS=http://localhost:9000 make run
```
MCP 实现遵循 2025-06-18 规范：`initialize` 握手 → `notifications/initialized` → `tools/list` / `tools/call`，stdio + Streamable HTTP 传输。

## 6. 方式 F：WASM 沙箱工具（可选）

```bash
# 构建样例 WASM 工具（需 TinyGo）
tinygo build -o plugins/wasm/bin/wordcount.wasm -target wasi plugins/wasm/wordcount
WASM_DIR=./plugins/wasm/bin make run   # 网关加载并在沙箱中执行
```
默认沙箱**无文件/网络权限**；通过 `Permissions` 显式授权（`ReadOnlyDirs`）才放开。原生工具（calculator/weather/read_file/code_review）开箱即用，无需 WASM。

## 7. 方式 G：Docker

```bash
make docker          # 构建镜像
make up              # docker compose 起全套（gateway + registry + math-agent）
make down
```

## 8. 测试与检查

```bash
make test            # go test ./... —— 路由引擎/背包/熔断/混合检索/编排端到端/求值器
make vet             # go vet
make bench-router    # 路由引擎量化
```

## 9. 关键配置（环境变量）

| 变量 | 默认 | 说明 |
|------|------|------|
| `LLM_PROVIDER` | `mock` | mock/qwen/openai/ollama |
| `EMBEDDING_PROVIDER` | `mock` | mock/dashscope/openai |
| `ROUTER_TOKEN_BUDGET` | `1500` | 工具 schema token 预算（背包容积） |
| `ROUTER_TOP_K` | `5` | 装箱后返回工具数上限 |
| `ROUTER_DENSE_W/_SPARSE_W/_CAP_W` | 0.6/0.3/0.1 | 混合检索融合权重 |
| `LB_STRATEGY` | `least_response_time` | round_robin/random/least_connections/weighted_rr/consistent_hash/least_response_time |
| `BREAKER_FAIL_THRESHOLD` / `BREAKER_OPEN_SECS` | 5 / 30 | 熔断器 |
| `METRICS_ADDR` | `:9090` | （与 HTTP 共址时走 /metrics） |
| `OTLP_ENDPOINT` | — | OTel trace 导出（留空则 trace 输出到日志） |

## 10. 常见问题

- **`go build` 报 `cannot find package ...rpc/pb`**：`internal/rpc` 与 `cmd/cli` 用 `//go:build grpc` 隔离，默认 `go build ./...` / `go test ./...` 无需 protoc 即可通过（gRPC 代码被跳过）。要启用 gRPC 四模式：`make proto`（生成 `internal/rpc/pb`）后 `make grpc`。
- **MCP/A2A 是手写实现还是用官方 SDK？**：按官方规范（MCP 2025-06-18 / A2A 2025）手写的 JSON-RPC 实现（`internal/mcp`、`internal/a2a`），未引入 MCP/A2A Go SDK —— 这样协议握手/传输/状态机的每处细节都自研可控。外部依赖只有 wazero / grpc / otel / prometheus。
- **WASM 工具加载失败**：确认用 `tinygo ... -target wasi` 构建；不装 TinyGo 也可用原生工具。
- **天气工具返回「离线降级」**：本机无外网时的预期行为（open-meteo 不可达）。
- **端口占用**：改 `GATEWAY_HTTP_ADDR` / `AGENT_PORT` / `REGISTRY_ADDR`。
