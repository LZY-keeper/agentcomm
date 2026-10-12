# 编译与运行指南（v3）

`agentcomm` 用 Go 1.22+ 实现（v3 开发基线 Go 1.25），纯 Go（无 cgo），跨平台单二进制。**默认离线可跑**（内置确定性 mock LLM/embedding，无需任何 API key）。

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
make test            # go test ./... —— 路由引擎/背包/熔断/混合检索/编排端到端/求值器 + 评测元测试
make vet             # go vet
make bench-router    # 路由引擎量化（6 条 smoke：token 节省 + recall@5/MRR）
make eval            # 完整评测（140 例金标：router 100 + e2e 40）→ eval/reports/
make eval-gate       # 评测回归门禁（对比 baseline，指标回退 exit 1，CI 同款）
make eval-baseline   # 重建基线（git diff report.json 逐指标评审）
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
| `EVAL_REPORT_DIR` | `eval/reports` | 评测报告根目录；playground 读 `<dir>/latest/report.json`（只读） |

## 10. 常见问题

- **`go build` 报 `cannot find package ...rpc/pb`**：`internal/rpc` 与 `cmd/cli` 用 `//go:build grpc` 隔离，默认 `go build ./...` / `go test ./...` 无需 protoc 即可通过（gRPC 代码被跳过）。要启用 gRPC 四模式：`make proto`（生成 `internal/rpc/pb`）后 `make grpc`。
- **MCP/A2A 是手写实现还是用官方 SDK？**：按官方规范（MCP 2025-06-18 / A2A 2025）手写的 JSON-RPC 实现（`internal/mcp`、`internal/a2a`），未引入 MCP/A2A Go SDK —— 这样协议握手/传输/状态机的每处细节都自研可控。外部依赖只有 wazero / grpc / otel / prometheus。
- **WASM 工具加载失败**：确认用 `tinygo ... -target wasi` 构建；不装 TinyGo 也可用原生工具。
- **天气工具返回「离线降级」**：本机无外网时的预期行为（open-meteo 不可达）。
- **端口占用**：改 `GATEWAY_HTTP_ADDR` / `AGENT_PORT` / `REGISTRY_ADDR`。

## 11. v3 新增能力速览

### HITL 人工审批
```bash
TOOLS_REQUIRE_APPROVAL=calculator make run
# Playground「计算 1+2」→ 审批条 → 批准/拒绝
# REST: POST /api/tasks → GET /api/tasks/{id} → POST /api/tasks/{id}/approval
# 取消: POST /api/tasks/{id}/cancel ；SSE 回放: GET /api/tasks/{id}/events
```

### 治理层（GOV_*)
```bash
KEY_HASH=$(python -c "import hashlib;print(hashlib.sha256(b'sk-my-key').hexdigest())")
GOV_ALLOW_LOOPBACK=false GOV_KEYS="team-a:$KEYHASH:tenant-a:5:10:200000|read_file,calculator|@builtin" make run
# 内联格式: id:sha256:tenant:rps:burst:budget[|tool白名单][|@server白名单]
# budget<0 无限；JSON 文件形态见 GOV_KEYS_FILE
```

| 变量 | 默认 | 说明 |
|------|------|------|
| `GOV_KEYS` / `GOV_KEYS_FILE` | — | virtual key 配置（未配置=治理关闭） |
| `GOV_ALLOW_LOOPBACK` | true | 本机豁免鉴权（生产置 false） |
| `GOV_RPS` / `GOV_BURST` | 5 / 10 | 每 key 限流默认值 |
| `GOV_BUDGET_TOKENS` | 200000 | 每 key token 预算（<0 无限；超限 402） |
| `GOV_MAX_QUERY_BYTES` | 32768 | 请求体上限 |
| `TOOLS_REQUIRE_APPROVAL` | — | 逗号分隔工具名，强制人工审批 |
| `TOOL_MAX_RESULT_BYTES` | 16384 | 工具结果截断上限 |
| `TOOL_MAX_FILE_BYTES` | 1048576 | read_file 大小上限 |
| `TOOL_FINGERPRINTS_FILE` | — | anti-rug-pull 指纹文件（加载+落盘目标） |
| `STORE_BACKEND` | auto | memory / redis（auto=按 REDIS_ADDR） |
| `APPROVAL_TIMEOUT` | 3600 | 审批 park 超时（秒），超时保持 input-required |
| `PUBLIC_URL` | — | A2A 卡片对外 URL |

### 协议对齐
- A2A v1.0：`/.well-known/agent-card.json` 带 `X-AgentCard-Signature`
  （Ed25519，公钥 `/.well-known/jwks.json`）；`supportedInterfaces` 声明
  1.0 与 0.3 兼容；`message/send` 返回 Task 对象；`tasks/get|cancel`。
- MCP：server 按客户端 requestedVersion 协商 2025-06-18 / 2025-11-25 /
  2026-07-28；网关自身暴露 `POST /mcp`。

### 任务持久化
```bash
REDIS_ADDR=127.0.0.1:6379 make run   # 审批任务跨重启可恢复（启动时自动 RecoverPending）
```

## 12. 评测体系（v3.1：金标集 / 报告 / 回归门禁）

「agent 项目没有评测系统就是玩具」——v3.1 补齐这一环。目录约定见 `eval/reports/README.md`。

### 构成

| 组件 | 位置 | 说明 |
|------|------|------|
| 路由金标集 | `eval/datasets/router.v1.yaml` | 100 例「查询→期望工具集」，中英 6:4，含 distractor 干扰例 |
| E2E 金标集 | `eval/datasets/e2e.v1.yaml` | 40 例端到端任务（数学/HITL 审批双路/脚本化路径） |
| 指标 | `internal/eval/metrics.go` | recall@k / precision@k / MRR / hit@1 / token 节省；E2E：成功率/终态/答案/工具轨迹/意图 |
| 报告 | `eval/reports/` | 版本化 JSON+Markdown；`StableDigest`（metrics 子树 sha256）双跑一致可验证 |
| 门禁 | `eval/thresholds.yaml` + `internal/eval/gate.go` | floor 绝对红线 + 相对基线最大跌幅（默认 2pp），CI exit 1 |

### 确定性保证（评测可复现的前提）

- mock LLM/embedding 离线运行（FNV-1a 确定性哈希，同文本同向量）；评测禁用 Beta bandit（NoopFeedback）；
- hybrid 排序 (Score desc, ToolID asc) 全序决胜——消除 map 遍历序导致的并列分漂移（该缺陷即由评测双跑 digest 不一致暴露后修复）；
- 门禁只比较 metrics 子树：时间戳/延迟不参与；`EVAL_REPORT_DIR` 只读暴露给 playground。

### 首基线实测（2026-10，12 工具联邦 / 140 例）

| 指标 | 实测 |
|------|------|
| router recall@5 | 0.92 |
| router MRR | 0.73 |
| router hit@1 | 0.62 |
| router token 节省均值 | 72.5% |
| e2e success_rate | 1.00（40 例，含 HITL approve/deny 双路） |
| e2e intent_accuracy | 1.00（29 例断言 intent） |

### 评测驱动的真实修复（面试讲点）

1. capability/intent 关键词词典同义词缺口（「摄氏度/下雨/带伞/save/dump」未映射）→ recall@5 从 0.685 迭代到 0.92；
2. mock extractExpr 只剥前缀不剥中文后缀，「123 + 456 等于多少」把「等于多少」喂进 calculator 解析失败；
3. calculator 不认中文算词（「7 减 10」）→ normalizeExpr 归一；
4. hybrid 并列分受 map 遍历序影响 → 排序加 ToolID 决胜，双跑 digest 由漂移变为一致。
