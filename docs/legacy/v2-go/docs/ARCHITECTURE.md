# 架构设计

## 一句话定位

**agentcomm** 是位于「LLM 应用」与「多 Agent（A2A）+ 海量 MCP 工具」之间的**治理与路由层**：对上暴露一个「超级 Agent」单一入口，对下用**语义 + token 预算感知的工具路由**、**成本/可靠性感知编排**、**WASM 沙箱工具执行**，把分散能力整合为低成本、高可靠、可治理的统一 AI 网关。

## 三个技术支柱

```
                 ┌──────────────────────────────────────────────┐
   用户 / 上游    │  Web Playground  ·  gRPC(4模式)  ·  A2A 端点   │
   ───────────▶ │           super-agent gateway                 │
                 └──────────────────────────────────────────────┘
                          │                  │                  │
            ┌─────────────▼──────┐ ┌─────────▼────────┐ ┌────────▼─────────┐
            │ 支柱二：编排        │ │ 支柱一：工具路由  │ │ 支柱三：WASM 沙箱 │
            │ 意图→发现→路由→     │ │ 混合检索+token    │ │ wazero 运行时     │
            │ 熔断→真 token 流    │ │ 背包装箱+反馈     │ │ 权限隔离执行工具  │
            └────────────────────┘ └──────────────────┘ └──────────────────┘
                          │                  │                  │
                  ┌───────▼──────────────────▼──────────────────▼──────┐
                  │ Agent（A2A, 本地/远端） · MCP 工具（原生/外部/WASM）  │
                  │ Registry（发现） · LLM/Embedding Provider（可插拔）   │
                  └────────────────────────────────────────────────────┘
```

### 支柱一：语义 + token 预算感知的联邦工具路由引擎（`internal/router`）
攻克「MCP 工具泛滥 → token 爆炸」（50+ 工具每次请求吃 5K+ token）。管线：
1. **联邦发现** `federation.go`：聚合多 MCP server 工具到统一注册表（`ServerID` 标记来源）。
2. **混合检索** `hybrid.go`：稠密向量（语义）⊕ BM25（`bm25.go`，精确词）⊕ 能力图（`capability.go`，I/O 类型兼容）→ min-max 归一化后**加权融合**（可配权重）。正交信号互补，召回与精度均优于单一稠密。
3. **token 预算背包装箱** 🌟 `packer.go`：给定 LLM token 预算，按「价值分 / schema 体积」做 **0/1 背包 DP**，选出价值最优且不超预算的工具子集——既非忽略体积的朴素 top-k，也非忽略价值的截断。
4. **跨 server 去重** `dedup.go` + **bandit 反馈** `feedback.go`（Beta 后验重排，按任务成败学习）。

输出 `Selection{FullTokens, PackedTokens, TokenSaved, TokenSavedPct}` —— 即简历的量化亮点。

### 支柱二：成本与可靠性感知的多 Agent 编排（`internal/orchestrator`）
- **意图分类** `intent.go` → **服务发现**（Registry tag 索引）→ **负载均衡** `lb.go`（6 策略，移植 v1，修一致性哈希）→ **熔断器** `breaker.go`（closed/open/half-open 真状态机）→ **真 token 流式**。
- **agentic 工具循环** `loop.go`（ReAct 风格）：注入背包选出的工具 → LLM 流式；模型请求工具调用时执行 → 回填结果 → 再提示，直到 stop。
- **等价降级**：agent/工具不可达 → 熔断 + fallback。`a2a_agent.go` 让远程 A2A agent 与本地 agent 统一接口。

### 支柱三：WASM 沙箱工具运行时（`internal/wasm`）
- 第三方/不可信工具编译为 `.wasm`，**wazero**（纯 Go、无 cgo）执行 → 跨平台单二进制（替换 v1 的 LoadLibrary/fork 互锁死）。
- **运行时级沙箱**：默认无文件/网络权限，`Permissions` 显式授权（WASI 只读挂载）才放开——直接回应 MCP 第三方工具安全风险。
- 原生 Go 工具（`internal/tools`：calculator/weather/read_file/code_review）为默认离线后端。

## 模块依赖（高内聚低耦合）

```
cmd/*  (依赖装配 / DI)
  └─ internal/
     ├─ playground   ─┐
     ├─ rpc (grpc)   ─┤  依赖 orchestrator / router / tools / registry / a2a / mcp
     ├─ a2a          ─┤
     ├─ orchestrator ─┼─ 依赖 router / tools / llm / registry / a2a
     ├─ router       ─┼─ 依赖 rag / embedding / types
     ├─ rag          ─┤
     ├─ wasm         ─┤  依赖 tools / types
     ├─ mcp          ─┤  依赖 tools / types
     ├─ tools        ─┤
     ├─ llm/embedding─┤
     ├─ registry     ─┤
     └─ config/logx/obs/types  (基础设施，被所有上层依赖)
```
规则：`internal/*` 仅经**接口**依赖，具体实现在 `cmd/` 装配；每包单一职责；无循环依赖。LLM/Embedding/MCP/ToolSource 都是接口，便于替换与测试。

## 工程能力（用户选定）
- **默认离线 mock provider**：`llm.Mock`（确定性真·逐 token 流，能合成工具调用）/ `embedding.Mock`（确定性哈希向量，离线 RAG 检索有意义）。免 key 跑通整条链路。
- **Web Playground**：SSE 流式 + 路由决策可视化（意图/agent/混合检索 top-k/token 装箱对比/工具调用/延时）。
- **真·端到端 token 流式**：channel 贯穿 LLM→orchestrator→A2A SSE→gRPC stream→Playground（替换 v1 的「切片+sleep」假流式）。
- **可观测性**：Prometheus `/metrics`（含**工具选择 token 节省量、检索召回、成本、熔断状态**等业务指标）+ OpenTelemetry 全链路追踪 + slog 结构化日志。

## v1（C++ 原作） vs v2（Go） 关键对比

| 维度 | v1 C++ | v2 Go |
|------|--------|-------|
| 工具选择 | 朴素 top-k 余弦 | 混合检索 + 能力图 + **token 背包装箱** + bandit |
| 工具执行 | LoadLibrary(Win) / fork(POSIX) 互锁死 | **WASM 沙箱**（wazero，跨平台+权限隔离） |
| 流式 | 切片+sleep 假流式 | 真 token 流（channel 端到端） |
| 异步 | 同步包装假异步 | 真 goroutine/channel 并发 |
| MCP | 第三方开源 server + 不合规 client | 自实现 2025-06-18（initialize+Streamable HTTP） |
| 指标 | 无 `/metrics` 端点，CPU/内存死代码 | Prometheus 业务指标 + OTel 追踪 |
| 可运行 | Linux + 重依赖 | 单二进制跨平台，默认离线免 key |

## 量化（系统内置测量，`make bench-router` / `/metrics`）
- 工具选择 token 占用 **↓ 70%+**（全量 vs 背包装箱，见 `agentcomm_router_tokens_total{phase=full|packed}`）。
- 混合检索召回/精度 优于纯稠密（离线评测）。
- 单查询成本/延时/P50 首 token 可观测。
- 等价降级可用性提升（熔断 + fallback）。
