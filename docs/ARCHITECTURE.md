# 架构设计（v3）

## 一句话定位

**agentcomm v3** 是位于「LLM 应用」与「多 Agent（A2A）+ 海量 MCP 工具」之间的**治理与路由层**：对上暴露一个「超级 Agent」单一入口，对下用**语义 + token 预算感知的工具路由**、**成本/可靠性感知编排**、**WASM 沙箱工具执行**，并在 v3 补齐企业上线必需的**任务持久化 + 人工审批（HITL）**、**治理层（virtual key / 限流 / 预算 / 审计 / 工具权限）**与 **2026 协议对齐（A2A v1.0、MCP 2025-11-25/2026-07-28、anti-rug-pull）**。

## 三支柱 + 两横层（v3）

```
                 ┌────────────────────────────────────────────────────────────┐
   用户 / 上游    │ Web Playground · gRPC(4模式) · A2A v1.0 端点 · MCP /mcp 端点 │
   ───────────▶ │                 super-agent gateway                        │
                 │ ┌──────────── 治理横层：Auth→RateLimit→Budget→Audit ───────┐│
                 │ └──────────── 任务横层：Hub/TaskRunner（持久化+HITL）──────┘│
                 └────────────────────────────────────────────────────────────┘
                          │                  │                  │
            ┌─────────────▼──────┐ ┌─────────▼────────┐ ┌────────▼─────────┐
            │ 支柱二：编排        │ │ 支柱一：工具路由  │ │ 支柱三：WASM 沙箱 │
            └────────────────────┘ └──────────────────┘ └──────────────────┘
                          │                  │                  │
                  ┌───────▼──────────────────▼──────────────────▼──────┐
                  │ Agent（A2A v1.0, 本地/远端） · MCP 工具（原生/外部/WASM）│
                  │ TaskStore/SessionStore（memory/redis） · LLM/Embedding │
                  └────────────────────────────────────────────────────┘
```

### 支柱一：联邦工具路由引擎（`internal/router`）— 沿承 v2
联邦发现 → 混合检索（dense ⊕ BM25 ⊕ capability graph，min-max 归一加权）→ Beta bandit 反馈 → 跨 server 去重 → **token 预算 0/1 背包装箱**。量化输出 `Selection{FullTokens, PackedTokens, TokenSavedPct}`（`make bench-router`）。

**v3 新增 — anti-rug-pull 指纹 pin（`fingerprint.go`）**：每个工具定义（name+description+schema）入索引时计算 sha256 并固定；后续 Refresh 发现漂移（经典 rug-pull / tool-poisoning 攻击向量）即 ERROR 级审计日志 + 指标，strict 模式（`TOOL_FINGERPRINTS_FILE` + strict）直接拒绝漂移定义进入索引。运维流程：审批漂移后重签指纹文件。

### 支柱二：多 Agent 编排（`internal/orchestrator`）— 沿承 + 深化
意图分类 → 服务发现 → LB（6 策略）→ 熔断 → ReAct 工具循环 → 真流式。

**v3 修复**：`AgentStats`（`stats.go`）以 EMA 维护每个 agent 的实测延迟/成功率并回填 `AgentInfo`，LB 的 least_response_time 从"名不副实"变为真实数据驱动。

**v3 新增 — LoopHooks（`loop.go`）**：`OnBeforeTool`（执行前裁决：审批 park）与 `OnAfterTool`（结果改写：超限截断）两个钩子点；`RunAgentLoop` 保持 variadic 兼容，v2 测试零改动。

### 任务横层：持久化 + HITL（`internal/store` + `internal/approval` + `task.go`）
- **TaskStore/SessionStore**：内存 TTL 实现（默认，`Clock` 注入可测）与 Redis 实现（`REDIS_ADDR` 时启用，Lua CAS + ZSET 索引）。Task 状态机对齐 A2A：`submitted→working→input-required→completed/failed/canceled`。
- **审批流**：工具注册时 `WithRequireApproval()`（或 `TOOLS_REQUIRE_APPROVAL` 环境变量批量标记）。ReAct 循环命中时：写 checkpoint（历史+pending call）→ 任务转 input-required → `approval.Gate.Park` 阻塞等裁决。
- **两条恢复路径**：进程内有 waiter → `Gate.Deliver` 快路径（零重付 LLM 成本）；无 waiter（重启后）→ `Hub.Resume` 从 checkpoint 续跑慢路径（Redis 存储可跨重启恢复，`Hub.RecoverPending` 启动时自动重挂）。
- **断线不停任务**：TaskRunner 的 ctx 脱离 HTTP 请求生命周期（`context.WithoutCancel`），tee 消费端断开仅停止转发；结果持久化在 TaskRecord，可 `GET /api/tasks/{id}` 或 SSE 重连。
- **Abort ≠ Deny**：任务取消/关闭唤醒 parked waiter 时返回 `ErrAborted`，不落"denied"票据，语义与人工拒绝严格区分。
- **单写者原则**：TaskRecord 的全部变更只发生在 runner goroutine；HTTP 侧仅 Create/Get/List 或经 Gate 投递——无锁竞争，rev CAS 只是跨进程兜底。

### 治理横层（`internal/govern`）
以 2026 AI 网关企业基线（LiteLLM/Higress/Kong 开源面）为准：
- **Virtual key**（`GOV_KEYS`/`GOV_KEYS_FILE`）：id + sha256（明文不落内存）+ tenant + 每key rps/burst + token 预算 + **工具/服务器白名单**；
- **Auth 中间件**：Bearer/X-API-Key；豁免前缀 + loopback 默认放行（离线 demo 零摩擦；`GOV_ALLOW_LOOPBACK=false` 生产收紧）；未配置 key 时整层不安装；
- **限流**：x/time/rate 令牌桶，429 + Retry-After；
- **预算**：每 key token 计数（runner 完成时经 `UsageSink` 上报），超限 402；
- **审计**：结构化 `audit.tool_call` 事件（key/tenant/task/tool/args 摘要脱敏/结果大小/成败/耗时/request id）；
- 组装零路由侵入：`govern.Chain(mux, RequestID, Auth, RateLimit, Budget)` 包住原 mux。

### 支柱三：WASM 沙箱（`internal/wasm`）— 沿承 v2
wazero（纯 Go）执行 `.wasm` 工具，默认零权限、显式授权放开；原生 Go 工具为离线默认后端。v3 修复 `StdoutLimit` 字段缺失的编译缺陷。

## 协议对齐（2026-09）

| 协议 | v3 实现 |
|---|---|
| MCP | server 端版本协商（2025-06-18 / 2025-11-25 / 2026-07-28，按客户端 requestedVersion 应答）；无状态 HTTP 容忍 2026-07-28 的 sessionless 调用；client 端以 2025-11-25 发起并接受回退；网关新增 `/mcp` Streamable HTTP 端点对外暴露工具 |
| A2A | v1.0 Agent Card（protocolVersion 1.0 + `supportedInterfaces` 声明 1.0/0.3 + `capabilities.extendedAgentCard`）；`message/send` 返回规范 Task 对象；`tasks/get`、`tasks/cancel`、`agent/getAuthenticatedExtendedCard`；卡片 Ed25519 签名（`X-AgentCard-Signature`，公钥见 `/.well-known/jwks.json`；规范用 JCS 规范化 + JWS，本项目以"签名原始字节"等效实现，见 ROADMAP） |

## 安全清单（v3 落地项）
1. HITL 审批：敏感工具执行前人工裁决，票据持久化可审计；
2. anti-rug-pull：工具定义指纹 pin，漂移即告警/阻断；
3. 工具结果截断（默认 16KB）防上下文爆炸；read_file 大小上限（默认 1MB）+ 沙箱根校验；
4. 请求体上限（默认 32KB）防 DoS / prompt flooding；
5. virtual key 最小权限（工具白名单）+ 限流 + 预算；
6. 审计日志参数脱敏（password/token/secret/api_key 掩码）；
7. WASM 沙箱默认零权限；
8. loopback 豁免可关闭，X-Forwarded-For 不参与鉴权判定（防伪造）。

## 量化（`make bench-router` / `/metrics`）
- 工具选择 token 占用 ↓70%+（背包 vs 全量注入）；
- 混合检索召回/精度优于纯稠密；
- 单查询成本/延时/首 token P50 可观测；任务级 token 计量入 TaskRecord；
- 熔断 + fallback + stats 反馈环的可用性提升。

## ROADMAP（v3 明确不做）
MCP Tasks extension / elicitation / OAuth Resource Server 全套；Agent Skills 注册表；JCS 规范化签名；Postgres checkpoint 后端；gRPC pb 再生成（保持 build-tag 隔离）。
