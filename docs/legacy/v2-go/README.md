# agentcomm — 企业级智能 Agent 网关 / MCP 工具路由引擎

> Go v2 reimplementation of the agent-communication project (C++ v1 preserved in the
> repository root as the original work). See `docs/ARCHITECTURE.md` and the project
> plan for the full rationale.

A **governance & routing layer** between LLM applications and a federation of agents
(A2A) + MCP tools. Three pillars:

1. **语义 + token 预算感知的联邦 MCP 工具路由引擎** — hybrid retrieval (dense ⊕ BM25
   ⊕ capability graph) + RRF fusion + **token-budget-aware knapsack packing** +
   cross-server dedup + bandit feedback. Attacks the "tool sprawl → token explosion"
   problem (50+ tools eat 5K+ tokens per call) head-on.
2. **成本/可靠性感知的多 Agent 编排** — intent → discovery → cost/SLA-aware routing →
   circuit-breaker fallback → true token streaming (A2A official SDK).
3. **WASM 沙箱工具运行时** — tools run as `.wasm` via `wazero` (pure Go, no cgo):
   cross-platform + runtime-level sandboxing + WASI permission scoping — addressing
   MCP's third-party-tool security risk.

Runs **offline by default** (deterministic mock LLM/embedding, no API keys); real
Qwen / DashScope / OpenAI / Ollama providers are pluggable.

## Quick start

```bash
make tidy        # first time, needs network to fetch deps
make run         # offline demo: gateway + agents + mcp server + playground at :8080
```

See `docs/BUILD.md` for details (offline / real-key / Docker).

## Status

Building milestone-by-milestone (M0–M8). See the project plan.
