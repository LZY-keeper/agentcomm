# agentcomm v3 — 企业级智能 Agent 网关 / MCP 工具路由引擎

> v3 迭代自 Go v2，v1 为 C++/gRPC 原作——两个前代版本的源码快照保留在
> `docs/legacy/`（三版本演进叙事见 `docs/legacy/README.md`）。
> v3 核心增量：**任务持久化 + 人工审批（HITL）**、**企业治理层（virtual key /
> 限流 / 预算 / 审计 / 工具权限）**、**2026 协议对齐（A2A v1.0 卡片签名、MCP
> 2025-11-25/2026-07-28 协商、anti-rug-pull 工具指纹）**、**v3.1 评测子系统
>（金标集 / 版本化报告 / CI 回归门禁）**。

[![ci](https://github.com/LZY-keeper/agentcomm/actions/workflows/ci.yml/badge.svg)](https://github.com/LZY-keeper/agentcomm/actions/workflows/ci.yml)

A **governance & routing layer** between LLM applications and a federation of
agents (A2A) + MCP tools. Three pillars + two horizontal layers:

1. **联邦 MCP 工具路由** — hybrid retrieval (dense ⊕ BM25 ⊕ capability) +
   token-budget knapsack packing + cross-server dedup + bandit feedback +
   **v3: tool-definition fingerprint pinning (anti-rug-pull)**.
2. **多 Agent 编排** — intent → discovery → LB（6 策略，v3 起由实测 EMA
   延迟/成功率驱动）→ circuit-breaker → ReAct loop（v3 支持审批 park/恢复）→
   true token streaming。
3. **WASM 沙箱** — wazero（纯 Go）执行第三方工具，默认零权限。
4. **任务横层（v3）** — TaskStore/SessionStore（memory/redis）、审批 Gate、
   checkpoint 恢复、断线不停任务。
5. **治理横层（v3）** — virtual key 鉴权、每 key 限流（429）与 token 预算
  （402）、MCP 工具白名单、脱敏审计日志。

Runs **offline by default**（确定性 mock LLM/embedding，免 key 免网络）；
Qwen / DashScope / OpenAI / Ollama 可插拔。

## Quick start

```bash
make tidy        # first time, needs network
make run         # http://localhost:8080 — playground / metrics / A2A / MCP
```

### 离线体验 HITL（人工审批）

```bash
TOOLS_REQUIRE_APPROVAL=calculator make run
# Playground 输入「计算 1+2」→ 右侧出现审批条 → 批准/拒绝 → 任务继续
# 或走 REST：
#   POST /api/tasks {"query":"compute 1+2"}
#   GET  /api/tasks/{id}            → state=input-required + approval 票据
#   POST /api/tasks/{id}/approval {"decision":"approve"}
```

### 启用治理层（生产形态）

```bash
KEY_HASH=$(python -c "import hashlib;print(hashlib.sha256(b'sk-my-key').hexdigest())")
GOV_ALLOW_LOOPBACK=false GOV_KEYS="team-a:$KEYHASH:tenant-a:5:10:200000" make run
# 无 key → 401；超速 → 429 + Retry-After；超预算 → 402；工具调用全量审计日志
```

### Redis 持久化（审批跨重启可恢复）

```bash
REDIS_ADDR=127.0.0.1:6379 make run
```

### 评测体系（v3.1）

```bash
make eval          # 140 例金标（router 100 + e2e 40）：recall@5=0.92 / MRR=0.73 / token ↓72.5% / e2e 100%
make eval-gate     # 回归门禁：对比基线，指标回退超 2pp 即 exit 1（CI 强制）
make eval-baseline # 重建基线（PR 逐指标 diff 评审）
```

### 其它端点

- `GET /healthz`、`GET /metrics`（Prometheus）
- `GET /api/eval/report`（只读评测报告，playground「评测报告」页签数据源）
- A2A v1.0：`GET /.well-known/agent-card.json`（Ed25519 签名头，公钥
  `/.well-known/jwks.json`）；`POST /a2a/`（message/send|stream、tasks/get、
  tasks/cancel、agent/getAuthenticatedExtendedCard）
- MCP：`POST /mcp`（initialize 协商 2025-06-18/2025-11-25/2026-07-28 →
  tools/list → tools/call）
- 路由引擎量化：`make bench-router`

See `docs/BUILD.md` for details, `docs/ARCHITECTURE.md` for design, and
`docs/ROADMAP.md` for what is deliberately out of scope.
