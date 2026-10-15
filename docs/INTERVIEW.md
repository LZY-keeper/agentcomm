# AgentComm 项目全解读 + 模拟面试指南

> **阅读对象**：准备面试的你。本文档 = 项目完全解读（第 1~9 章）+ 面试官视角问答（第 10 章）。
> 读法建议：第 1~3 章建立全局叙事，第 4~8 章是每个模块的"是什么/为什么/怎么做"，第 9 章是量化与验证，第 10 章按主题背要点。标 ⭐ 的是面试官最可能深挖的地方。

---

## 目录

1. [一句话定位与项目背景](#1-一句话定位与项目背景)
2. [三版本演进叙事（简历主线）](#2-三版本演进叙事简历主线)
3. [总体架构：三支柱 + 两横层](#3-总体架构三支柱--两横层)
4. [支柱一：联邦工具路由引擎（router）](#4-支柱一联邦工具路由引擎router)
5. [支柱二：多 Agent 编排（orchestrator）](#5-支柱二多-agent-编排orchestrator)
6. [支柱三：WASM 沙箱工具运行时（wasm）](#6-支柱三wasm-沙箱工具运行时wasm)
7. [任务横层：持久化 + 人工审批 HITL ⭐](#7-任务横层持久化--人工审批-hitl-)
8. [治理横层（govern）⭐](#8-治理横层govern-)
9. [协议对齐、可观测性、测试与部署](#9-协议对齐可观测性测试与部署)
10. [模拟面试：面试官问答 60 问](#10-模拟面试面试官问答)

---

## 1. 一句话定位与项目背景

**AgentComm 是位于「LLM 应用」与「多 Agent（A2A）+ 海量 MCP 工具」之间的治理与路由层（Gateway）**。

它不是又一个 Agent 框架，而是解决企业把 Agent 推上生产时绕不开的三类问题：

| 痛点 | 本项目方案 |
|---|---|
| 工具太多 → 每次调用把全部工具 schema 塞进上下文，token 爆炸 | 支柱一：混合检索 + token 背包装箱，只注入"最值得"的少数工具（**↓70%+**） |
| Agent 不可靠：下游 agent 挂了/慢了没有兜底 | 支柱二：意图分类 → LB 六策略 → 熔断 fallback → 真·流式 ReAct 循环 |
| 第三方工具不可信：恶意/越权/静默投毒 | 支柱三：WASM(wazero) 沙箱零权限执行 + anti-rug-pull 定义指纹 pin |
| 敏感动作（写文件、删库）无人把关 | 任务横层：任务持久化 + 人工审批（HITL），审批可跨重启恢复 |
| 调用方失控：谁在用？花多少？能调什么？ | 治理横层：virtual key → 鉴权 → 限流 → token 预算 → 脱敏审计 |
| 协议快速演进（MCP 2025-06 → 2026-07、A2A 0.3 → 1.0） | 三版本 MCP 协商、A2A v1.0 卡片 Ed25519 签名 + JWKS |

**形态**：一个纯 Go 单二进制网关（无 cgo），默认离线可跑（内置确定性 mock LLM/embedding，零 API key），接真实 Qwen/OpenAI/Ollama 只需换环境变量。

**技术栈**：Go 1.25 ｜ MCP / A2A（按官方规范手写 JSON-RPC，不用 SDK）｜ wazero ｜ Prometheus + OpenTelemetry ｜ gRPC 四模式（build-tag 隔离）｜ Redis ｜ x/time 令牌桶 ｜ Ed25519

**规模**：约 10,300 行 Go（`internal/` 约 60 文件），8 个测试文件全绿，`make bench-router` 可现场复现量化收益。

---

## 2. 三版本演进叙事（简历主线）

这是面试 2 分钟自我介绍项目的主线，体现 **"能造系统 → 能重构系统 → 能交付系统"**：

```
C++ v1（原创，docs/legacy/v1-cpp/）  Go v2（docs/legacy/v2-go/，档案）    Go v3（仓库根，最终形态）
─────────────────────────        ─────────────────────────        ─────────────────────────
从零实现多 Agent 通信框架          复盘 v1 工程局限后生产级重写          站在"生产上线"视角企业化迭代
gRPC + libcurl + nlohmann/json    三支柱架构确立：                     · 任务持久化 + HITL 审批闭环
MCP 集成 + RAG-MCP 工具选择        · 混合检索 + token 背包路由           · govern 治理层（key/限流/预算/审计）
内存注册中心 + Orchestrator        · 真·流式（修掉 v1 假流式）           · A2A v1.0 + Ed25519 卡片签名
C++17 / 跨平台痛苦 / 无指标        · WASM 沙箱（wazero 纯 Go）          · MCP 三版本协商 + anti-rug-pull
                                 · 可观测性（Prometheus/OTel）         · AgentStats EMA 反馈环等可靠性修复
```

**每个版本被下一代取代的原因（必须会说）**：
- v1 → v2：C++ 工程局限——"假流式"（先算完再切片 sleep 模拟）、跨平台构建痛苦、没有指标体系、动态库插件不安全 → 用 Go 重写，channel 强制真流式，wazero 换掉 LoadLibrary。
- v2 → v3：v2 是"功能完整的 demo"，但离生产缺三样——审批闭环（敏感工具没人把关）、调用方治理（多租户下谁都能无限调用）、协议合规（MCP/A2A 规范 2025→2026 演进）→ v3 补齐这三块。

---

## 3. 总体架构：三支柱 + 两横层

```
                 ┌────────────────────────────────────────────────────────────┐
   用户 / 上游    │ Web Playground · gRPC(4模式) · A2A v1.0 端点 · MCP /mcp 端点 │
   ───────────▶ │                 super-agent gateway (:8080)                 │
                 │ ┌──────────── 治理横层：Auth→RateLimit→Budget→Audit ───────┐│
                 │ └──────────── 任务横层：Hub/TaskRunner（持久化+HITL）──────┘│
                 └────────────────────────────────────────────────────────────┘
                          │                  │                  │
            ┌─────────────▼──────┐ ┌─────────▼────────┐ ┌────────▼─────────┐
            │ 支柱二：编排        │ │ 支柱一：工具路由  │ │ 支柱三：WASM 沙箱 │
            │ internal/orchestrator│ │ internal/router │ │ internal/wasm    │
            └────────────────────┘ └──────────────────┘ └──────────────────┘
                          │                  │                  │
                  ┌───────▼──────────────────▼──────────────────▼──────┐
                  │ Agent（A2A v1.0 本地/远端）· MCP 工具（原生/外部/WASM） │
                  │ TaskStore/SessionStore（memory/redis） · LLM/Embedding │
                  └────────────────────────────────────────────────────┘
```

### 一次请求的完整生命周期（背下来，面试常问"讲一下请求流程"）

以 Playground 输入「计算 123+456」为例：

1. **治理链**（若配置了 GOV_KEYS）：`govern.Chain` 依次过 RequestID → Auth（Bearer/X-API-Key → sha256 查表 → Identity 进 ctx）→ RateLimit（令牌桶，429+Retry-After）→ Budget（超预算 402）。
2. **HTTP 层**：`POST /api/chat` → playground.Handler → `Hub.Start()` 创建 TaskRecord（state=submitted，checkpoint 带会话历史）→ 返回 SSE 流 + `X-Task-Id`。
3. **任务横层**：Hub 为任务 spawn 独立 runner goroutine，ctx 用 `context.WithoutCancel` 与 HTTP 请求解耦——**客户端断线任务不死**。SSE 只是 tee 的一份拷贝。
4. **支柱二编排**：`Orchestrator.Stream` → 意图分类（关键词规则，math=0.9）→ 按 skill 选 agent 池 → LB（默认 least_response_time，用 AgentStats 的实测 EMA）→ 熔断检查（open 则 fallback 其他健康 agent）。
5. **支柱一路由**：`router.Select` → 混合检索（dense⊕BM25⊕capability，min-max 归一加权 0.6/0.3/0.1）→ Beta bandit 调分 → 跨 server 去重 → **0/1 背包**按 token 预算装箱 → 得到 Selection{FullTokens, PackedTokens, TokenSavedPct}。
6. **ReAct 循环**（`RunAgentLoop`，最多 5 轮）：LLM 流式吐 token → 若发出 tool_calls：
   - `OnBeforeTool` 钩子：若工具 RequireApproval → **park**：checkpoint+审批票据落库 → 任务转 input-required → SSE 推 `input_required` 事件 → `Gate.Park` 阻塞等裁决；
   - 否则 invoker 执行工具（native / MCP 远端 / WASM 沙箱），记录 feedback/熔断/stats/审计；
   - `OnAfterTool` 钩子：结果超 16KB 截断；
   - 结果回填 history 重新提问，直到模型输出终止 token。
7. **收尾**：runner 累计 answer + input/output tokens → 任务转 completed → SessionStore 记忆本轮对话 → `UsageSink.ChargeTokens` 按 key 计费进预算。

---

## 4. 支柱一：联邦工具路由引擎（router）

**解决的核心问题**：MCP 生态下工具动辄几十上百个，全量注入 schema 每次调用吃 5K+ token。这是"tool sprawl → token explosion"问题，在网关层做价值最优选择。

### 4.1 五级流水线（`Select` 的执行顺序，要能默写）

```
Refresh（联邦发现+建索引）
   → Hybrid.Score（混合检索打分）
   → 阈值过滤（SimThreshold，默认 0.25）
   → Feedback.Adjust（Beta bandit 调分）
   → Dedup（跨 server 同名工具去重）
   → CandidateCap 截断（50，保证背包良态）
   → Packer.Pack（0/1 背包按 token 预算装箱）
   → TopK 截断（默认 5）
   → 量化输出 Selection
```

### 4.2 混合检索：三路信号 + min-max 归一融合（`hybrid.go`）⭐

| 信号 | 实现 | 捕捉什么 | 默认权重 |
|---|---|---|---|
| **dense** 稠密语义 | `rag.Retriever`：工具 fingerprint（name+desc+schema+I/O 类型）→ embedding → 暴力余弦 top-k | "查询是什么意思" | 0.6 |
| **sparse** 稀疏关键词 | 手写 **BM25**（k1=1.5, b=0.75，IDF = ln(1+(N-n+0.5)/(n+0.5))） | 精确标识符/术语匹配 | 0.3 |
| **capability** 能力图 | 查询意图 → 需要的输出类型（numeric/weather/code/file）∩ 工具声明的 I/O 类型 → 0/1 | "这个工具能产出需要的东西吗" | 0.1 |

**为什么混合**：三路信号正交。语义检索会被相似文本骗（"天气"和"气候新闻"），BM25 救精确命中，能力图救"文本不像但功能对"的候选。
**关键细节**：各路分数 **min-max 归一化到 [0,1]** 再加权——原始 BM25 分数量纲和余弦相似度完全不同，直接加权和会被某一路垄断（这是 raw weighted sum 的经典失败模式）。

### 4.3 token 预算 0/1 背包装箱（`packer.go`）⭐⭐ 项目最大算法亮点

- **建模**：每件物品 = 一个工具，**体积** = `EstimateTokens`（≈4 字符/token + 12 结构开销），**价值** = 融合分数；**背包容量** = `ROUTER_TOKEN_BUDGET`（默认 1500）。目标：预算内价值最大。
- **为什么不是 top-k**：top-k 无视工具大小——3 个高分大工具可能撑爆预算，而"4 个中小工具"总价值更高；为什么不是预算截断：无视价值。**只有背包两者兼顾且理论最优**。
- **实现**：一维 DP 滚动数组 `O(N·budget)`，`keep[i][w]` 布尔回溯矩阵还原选中集合；倒序枚举容量 `for w := budget; w >= 0; w--`（0/1 背包标准写法，防重复选取）。
- **优雅降级**：若连最优单工具都超预算，返回分数最高的那一个——agent 永远至少暴露最佳能力，而不是空手。

### 4.4 Beta bandit 反馈（`feedback.go`）

- 每个工具维护 Beta(α,β) 成功/失败计数（先验 1,1）；每次工具真实执行后 `Record(tool, ok)` 回填。
- `Adjust` 把后验均值 `α/(α+β)` 以 `weight*(mean-0.5)` 加回融合分（以 0.5 为中性中心），再重排。无历史的工具拿 0.5 中性分——天然鼓励探索。
- 面试点：这是**确定性利用**（posterior mean）而非 Thompson 采样；权重 0.15 说明它只是"微调"，防止小样本翻转主排序。key 用 tool name 而非 serverID/name（因为 invoker 只拿得到 name，且选择集已按 name 去重）。

### 4.5 anti-rug-pull 指纹 pin（`fingerprint.go`）⭐ v3 供应链安全

- **威胁**：上游 MCP server 在审批通过后**静默修改工具定义**（改 description 注入 prompt、改 schema 越权）——这是 MCP 生态经典的 rug-pull / tool-poisoning 攻击向量。
- **机制**：工具定义（server_id + name + description + schema.properties）入索引时算 **sha256 并 pin**；每次 `Refresh`（5 分钟定时 + 首次）重算比对，漂移即 ERROR 级审计日志 + `router_tokens{phase="drift"}` 指标。
- **两档策略**：strict 模式直接**拒绝漂移定义进入索引**（`TOOL_FINGERPRINTS_FILE` + strict）；非 strict 放行但告警。运维流程：人工审批漂移后重签指纹文件。
- 细节：指纹哈希不含 embedding 向量本身（向量随定义派生），锚定的是"声明"。

---

## 5. 支柱二：多 Agent 编排（orchestrator）

### 5.1 流水线

```
意图分类（Classifier，关键词规则，确定性离线；LLM 分类器是可插拔接口）
   → 服务发现（按 skill 过滤 agent 池；空则退化为全体）
   → LB.Select（6 策略，默认 least_response_time）
   → 熔断守卫（open → 遍历找健康 agent fallback）
   → router.Select 注入打包后的工具集
   → agent.Handle → RunAgentLoop（ReAct）
```

### 5.2 负载均衡 6 策略（`lb.go`）

`round_robin`（原子计数器）/ `random` / `least_connections`（ConnStart/ConnDone 原子计数在途调用）/ `weighted_rr`（**nginx 式平滑加权**：cw += w，选中者 cw -= total，权重来自成功率×10）/ `consistent_hash`（FNV-64 环，session 粘滞）/ `least_response_time`（按 LatencyP50MS 升序 + 成功率降序稳定排序）。
细节：`filterHealthy` 返回新切片而非别名——防止下游原地排序污染调用方切片（v1 踩过的坑）。

### 5.3 熔断器（`breaker.go`）

标准 closed → open → half-open 三态机，**每 agent 一个实例**（BreakerManager），互不连坐：
- closed：连续失败 ≥ 阈值（默认 5）→ trip；任一成功清零。
- open：拒绝所有调用 `openSecs`（默认 30s）；到期后半开。
- half-open：放行探测请求；**2 次成功**才闭合（防抖动），1 次失败立刻重新 open。
Orchestrator 在 breaker open 时遍历其余健康 agent 做 fallback——这是"可靠性感知"的落点。

### 5.4 ReAct 工具循环（`loop.go`）⭐

`RunAgentLoop(ctx, provider, system, req, tools, invoker, out, opts...)`：
- 流式消费 LLM token 直通 `out` channel（真流式到端）；遇 `ToolCalls` 标记则吞掉 marker、执行工具、以 tool 角色消息回填 history、重新 `Chat`；无工具调用即终止。上限 `maxIter=5`（防死循环），超限发 `[已达最大工具调用轮次]`。
- **LoopOpt variadic 设计**：`WithMaxIter / WithHooks / WithStartHistory / WithPending`——v2 调用方零改动升级 v3（向后兼容的 Go 惯用法）。
- **LoopHooks 两个干预点**（v3 新增，治理的挂载点）：
  - `OnBeforeTool(ctx, call) (ok, reason, err)`：执行前裁决。返回 ok=false 时把 reason 作为 tool 结果回喂模型（模型会改道而不是反复重试）；审批 park 在这里发生。
  - `OnAfterTool(ctx, call, res, err, dur) ToolResult`：结果改写。16KB 截断在这里。
  - hooks 经 **ctx 传递**（`WithLoopHooks`），因为 LLMAgent 在 hook 装配之前创建——用 ctx 注入避免了构造顺序问题。

### 5.5 AgentStats EMA 反馈环（`stats.go`）⭐ v2→v3 修复的代表

- v2 的问题：LB 的 least_response_time 读的是 AgentInfo 静态默认值——"名不副实"。
- v3：每次工具调用 `Observe(agentID, dur, ok)` → 指数滑动平均 `ema = 0.8*ema + 0.2*ms`；成功率带 **Beta(5,5) 先验** `(s+5)/(t+10)`（一次失败不会把成功率打成 0%）；窗口超 200 次 ×0.9 衰减（允许 agent 从早期失败中恢复）；ms 上限 30s 截断（超时毛刺不毒化 EMA）；成功率 <0.3 才标记 unhealthy（硬降级阈值）。
- `Apply(&info)` 在每次路由前把实测值回填 AgentInfo → LB 拿到真实数据。**闭环：执行 → 观测 → 回填 → 影响下次路由**。

---

## 6. 支柱三：WASM 沙箱工具运行时（wasm）

- **wazero**：纯 Go 的 WASM 运行时，无 cgo——保住"单二进制跨平台"，替代 v1 的 LoadLibrary/fork 插件方案。
- **信任模型**：native Go 工具（calculator/weather/read_file/code_review）是可信内置后端；WASM 是**不可信第三方工具**的执行路径。
- **沙箱承诺**：默认零权限——不挂 FS、不给网络、不透传 env；`Permissions.ReadOnlyDirs` 显式授权才放开（WASI preopen），且 read_file 路径有独立 root 校验。
- **ABI**：工具从 stdin 读 JSON 请求、往 stdout 写 `{is_error, text}` JSON——语言无关，TinyGo 可编译。
- **隔离强度**：**每次调用全新实例化模块**（无共享状态，调用间完全隔离）；`limitedBuffer` 给 stdout 上限 1MiB（防失控模块内存耗尽，超限即报错——深度防御）。
- 工程细节：WASI 的 exit(0) 在 wazero 里表现为 error，`isWasiExit0` 通过错误串识别并放行（已知 API 边界问题的务实处理）。

---

## 7. 任务横层：持久化 + 人工审批 HITL ⭐⭐

这是 v3 的灵魂，面试深挖概率最高。三个组件：`internal/store`（持久化）+ `internal/approval`（进程内信箱）+ `orchestrator/task.go`（Hub/TaskRunner 编排）。

### 7.1 任务状态机（对齐 A2A）

```
submitted → working → input-required? → completed / failed / canceled / rejected
                ▲            │
                └── 裁决续跑 ──┘（input-required 可长期驻留，checkpoint 在库）
```

`TaskRecord` = A2A Task（线格式投影）+ 网关私有字段：`Rev`（乐观锁版本）、`Checkpoint`（history + PendingCall + Iter）、`Approval`（审批票据：ID/工具/参数摘要/决定/决定人/时间）、`Answer`（累计流式输出）、`InputTokens/OutputTokens`（计量与预算执法依据）。

### 7.2 持久化双后端（`store.go` / `memory.go` / `redis.go`）

| | MemoryTaskStore（默认） | RedisStores（REDIS_ADDR 时启用） |
|---|---|---|
| TTL | 懒过期（读时删）+ 后台 janitor（30s 扫） | redis 原生 PX TTL |
| 并发 | RWMutex + **rev CAS 乐观锁**（rev 不匹配 → ErrConflict） | **Lua 脚本原子 CAS**（GET→比对 rev→+1→SET，-1 缺失/-2 冲突） |
| 索引 | bySess map（session → task ids） | ZSET 按创建时间评分 + TxPipeline |
| 重启 | 状态丢失（by design） | **审批可跨重启恢复** |
| 会话 | 滚动窗口 Load 时截断（maxTurns*2 条） | RPush + LTrim 512 + LRange |

设计关键：调用方只依赖 `TaskStore/SessionStore` 接口，cmd 层装配后端；`PendingTasks`（非接口方法）通过**接口断言**在 Hub.RecoverPending 里探测——只有 Redis 后端才有重启恢复能力，内存后端优雅降级为"找不到"。
`Clock` 接口注入时间——TTL 逻辑测试可确定性推进（不用 sleep 的测试）。

### 7.3 审批流：park → deliver/resume 双恢复路径 ⭐⭐

**命中 RequireApproval 工具时（`approvalHook`，在 OnBeforeTool 里）**：

1. 写 **checkpoint**（当前 history + pending call）+ 审批票据落库（参数摘要 256 字节 + 脱敏）；
2. 任务转 `input-required`；
3. 向 SSE 推一条 `ControlInputRequired` 控制令牌（让前端立刻弹审批条，**先于阻塞**——select default 非阻塞发送）；
4. `Gate.Park(ctx, taskID, timeout)` 阻塞 runner goroutine。

**裁决到达的两条恢复路径**（`taskapi.decide`）：

- **快路径（进程内）**：`Gate.Deliver(taskID, decision)` → 信箱直达 parked waiter → 裁决落票据 → 任务回 working → **零延迟续跑，不重付任何 LLM 成本**。
- **慢路径（跨重启）**：Deliver 返回 false（本进程无 waiter，如重启后）→ `Hub.Resume`：从 store 读 checkpoint → 快照 pendingCall → 清 checkpoint → 转 working → 重新 spawn runner，带 `WithStartHistory(ckpt.History) + WithPending(PendingResolution{Call, Approve})` 两个 LoopOpt → **循环开头先补记 tool_call 消息并执行/拒绝该 pending call，再进正常循环**——恢复语义精确到"续段"。
- 启动兜底：`Hub.RecoverPending` 扫描非终态任务，**已批未执行**的自动 Resume 重挂。

### 7.4 五个并发设计要点（面试官会挑一个往死里问）⭐⭐

1. **单写者原则**：TaskRecord 的所有变更只发生在该任务的 runner goroutine；HTTP 侧只 Create/Get/List，裁决经 Gate 信箱"流回" runner。**并发正确性靠架构（谁写谁负责）而非锁堆积**——rev CAS 只是跨进程兜底，不是日常同步原语。
2. **断线不停任务**：runner ctx = `context.WithoutCancel(base)`，与 HTTP 请求生命周期彻底解耦；SSE 只是 tee 消费者，断开只停转发（channel 满时 select ctx.Done 分支继续 drain），答案全量持久化在 TaskRecord.Answer，可 `GET /api/tasks/{id}` 或 SSE 重连续读。
3. **Abort ≠ Deny**：任务取消/Shutdown 唤醒 parked waiter 时，Gate 置 `aborted=true`，Park 返回 `ErrAborted`——**不落"denied"票据**。理由：取消是系统行为，拒绝是人的决定，混同会污染审批审计链。
4. **Gate 四路退出零泄漏**：Park 的 select 等 `w.ch`（裁决）/ `tm.C`（超时）/ `ctx.Done()`；defer 里关 `done` channel 并删 waiter；Deliver 用 `select { case w.ch <- d; case <-w.done }` 处理"waiter 恰好超时离开"的竞态（发不进也不阻塞）。每任务至多一个 waiter（重复 Park 直接 ErrTimeout，防御式）。
5. **park 而非终止重跑**：park 保住了 LLM 上下文（已生成的 token 不作废、零重付成本）；checkpoint 只为跨重启兜底——**checkpoint ≠ durable execution**，恢复语义由 WithPending/WithStartHistory 精确续段保证，而不是"从头重放事件日志"。

### 7.5 超时语义

`APPROVAL_TIMEOUT`（默认 1h）超时 → `finish(input-required, "approval not received")` → 任务**保持 input-required**（不转 canceled/failed），票据还在、checkpoint 还在，人工随时可裁决走 Resume。runner 收尾时还有一道保护：读最新落库状态，若已是 input-required 则直接 return（**不把 park 状态压平成 completed**——单写者跨段竞争的收尾守则）。

---

## 8. 治理横层（govern）⭐

对标 2026 AI 网关企业基线（LiteLLM / Higress / Kong 开源面），全部是**普通 net/http 中间件**——无框架、不接管路由，`govern.Chain(mux, RequestID, Auth, RateLimit, Budget)` 包住原 mux，端点零改动。

### 8.1 责任链顺序（顺序本身是面试点）

```
RequestID → Auth(401) → RateLimit(429) → Budget(402) → 业务 mux
```
先鉴权再限流：没有身份就无法按 key 限流；先限流再预算：便宜的错误先挡掉，预算检查要查账（代价高些）放最后。

### 8.2 Virtual key（`keys.go`）

- `KeyRecord{id, key_sha256, tenant, rps, burst, budget, tools[], servers[]}`。
- **明文 key 不落内存**：只有 SHA-256 哈希（GOV_KEYS 内联格式 `id:sha256:tenant:rps:burst:budget[|tool白名单][|@server白名单]` 或 GOV_KEYS_FILE JSON）。
- `Lookup`：哈希查表 + **subtle.ConstantTimeCompare 烧时间**对齐未命中路径（防时序侧信道）。
- `AllowsTool`：**空白名单 = 允许全部**（tenant 是硬边界，per-key 工具过滤是附加最小权限约束——域内 fail-open 的有意取舍）。
- 配置解析**聚合所有错误一次性报出**（fail fast at deploy time），重复哈希报错。

### 8.3 Auth / RateLimit / Budget / Audit

- **Auth**：Bearer → X-API-Key 提取；protected 前缀（/api,/a2a,/mcp）强制、exempt 前缀（/healthz,/metrics）放行；**loopback 默认放行**（离线 demo 零摩擦，`GOV_ALLOW_LOOPBACK=false` 生产收紧）；**X-Forwarded-For 刻意不参与判定**（直接 peer 才是代理，信任客户端头 = 鉴权绕过）；`prefixMatch` 边界感知（"/api" 不会匹配 "/apiv2-evil"）。命中后 Identity 进 ctx，并 `WithPrincipal` 把 key id 塞给任务层——**预算执法的记账主体靠 ctx 跨层传递**。
- **RateLimit**：x/time/rate 令牌桶，每 key 懒创建（key 记录可覆盖默认 rps/burst）；`Reserve()` 探测延迟再 `Cancel()` 取得 Retry-After；匿名流量共享 "_anon" 桶。拒绝：429 + Retry-After。
- **Budget**：`Used >= limit` → 402 Payment Required。`UsageSink` 接口由 Hub 在任务完成时 `ChargeTokens`（principal 从 ctx 来）——**预算执法的依据是任务级真实 token 计量**，不是估算。limit<0 = 无限。
- **Audit**：`AuditInvoker` 包 ToolInvoker，产出结构化 `audit.tool_call` 事件：request_id / key_id / tenant / task_id / tool / server / **args_summary（256 字节 + 脱敏）** / result_bytes / ok / duration_ms。
- **脱敏**：`MaskSecrets` 对 password/token/secret/api_key/apikey/authorization 六类键值替换 "***"（大小写不敏感扫描 JSON 键名）。

### 8.4 为什么默认关闭？

未配置 GOV_KEYS 时 auth 中间件**干脆不安装**。理由：企业功能与开发者体验不冲突——零配置离线可跑是采用率的前提；GOV_* 一开即生产形态。这是"默认开放、按需收紧"的分层安全设计。

### 8.5 v3 请求体/结果限额全景

| 限额 | 默认 | 防什么 |
|---|---|---|
| `GOV_MAX_QUERY_BYTES` / playground 32KB | 32768 | DoS / prompt flooding |
| taskapi body 1MB、approval body 4KB、A2A RPC body 1MB | — | 同上，按端点分级 |
| `TOOL_MAX_RESULT_BYTES`（OnAfterTool 截断） | 16384 | 工具结果撑爆 LLM 上下文/预算 |
| `TOOL_MAX_FILE_BYTES`（read_file 前置 stat） | 1MB | 大文件读爆内存 |
| WASM stdout limit | 1MiB | 失控模块 |

---

## 9. 协议对齐、可观测性、测试与部署

### 9.1 MCP：三版本协商 + Streamable HTTP ⭐

- **版本**：2025-06-18（v2 基线）/ 2025-11-25（官方 client 指南的当前默认）/ 2026-07-28（最新修订，无状态核心方向）。
- **server 端**：`initialize` 握手按客户端 requestedVersion 应答（`negotiateProtocol`：支持则回显，不支持回 2025-11-25——规范要求的"无交集回最新"策略）；无状态 HTTP 天然容忍 2026-07-28 的 sessionless 调用（每请求独立，无 Session-Id 依赖）。
- **client 端**（`mcp.Client`，联邦上游）：以 2025-11-25 发起 initialize、接受服务端回退；`sync.Once` 保证握手恰好一次；可解析 SSE 响应（取首个 `data:` 帧——三个所用方法各返回单帧，拼接反而会产生非法 JSON）。
- **为什么手写不用官方 SDK**：握手/传输/状态机每处细节自研可控，且外部依赖只剩 wazero/grpc/otel/prometheus/go-redis。（注意：go.mod 注释里提到的官方 SDK 仅是依赖备注，实际实现全部手写——面试如被问，答"按规范手写 JSON-RPC 子集"）

### 9.2 A2A v1.0（2026-04-09 规范）⭐

- 卡片 `/.well-known/agent-card.json`：`protocolVersion: 1.0` + **`supportedInterfaces` 同时声明 1.0 与 0.3.0**（v1.0 要求必须声明 1.0，0.3 是向后兼容窗口）+ `capabilities.extendedAgentCard`。
- **Ed25519 卡片签名**：`X-AgentCard-Signature`（base64）+ `X-AgentCard-Signature-Alg: ed25519`，签名**实际下发的原始字节**；公钥发布在 `/.well-known/jwks.json`（kty=OKP, crv=Ed25519）。**防卡片伪造/中间人篡改**。
- **已知偏差（面试要主动讲）**：v1.0 规范要求 JCS(RFC 8785) 规范化 + detached JWS；本项目以"签名原始字节"等效实现——**同样的防篡改保证，不引入 canonicalization 依赖**，规范兼容列入 ROADMAP。这是"明确知道规范细节、做出工程取舍并文档化"的好素材。
- RPC 面：`message/send`（经 Hub 持久化，返回规范 Task 对象 id/contextId/status/history）、`message/stream`（SSE delta）、`tasks/get`、`tasks/cancel`、`agent/getAuthenticatedExtendedCard`（扩展卡片多一个 tasks 技能；govern 已挡匿名，此处再加纵深防御注释）。

### 9.3 HTTP 表面一览（要能报出来）

| 端点 | 作用 |
|---|---|
| `/` | Playground（embed 静态页，SSE 流 + 路由决策面板） |
| `POST /api/chat` | SSE：decision → token/input_required → done；响应头 `X-Task-Id` |
| `POST/GET /api/tasks` | 创建任务（立即返回，后台 drain）/ 列表 |
| `GET /api/tasks/{id}` | 任务状态（含 answer/approval/checkpoint 投影） |
| `POST /api/tasks/{id}/approval` | HITL 裁决 `{decision, by, reason}` → {mode: in-process \| resume} |
| `POST /api/tasks/{id}/cancel` | 取消（Abort ≠ Deny） |
| `GET /api/tasks/{id}/events` | SSE 重连：重放已累计 answer（差量）+ state 轮询（300ms）+ 终态 done |
| `/.well-known/agent-card.json` + `jwks.json` | A2A v1.0 卡片 + 签名公钥 |
| `POST /a2a/` | A2A JSON-RPC |
| `POST /mcp` | MCP Streamable HTTP（把内置工具联邦输出） |
| `/healthz`, `/metrics` | 健康 / Prometheus |

### 9.4 可观测性（`obs.go`）

- **业务指标**（不只是 RED）：`agentcomm_router_tokens_total{phase=full|packed}`（差值即节省）、`router_packed_tools` 直方图、`tool_calls_total{tool,server,result}`、`orchestrator_route_total{agent,intent}`、`breaker_state` gauge、`llm_tokens_total`、`cost_milli_total`。
- **OTel**：OTLP endpoint 可配；未配置时 stdout 或 noop exporter（依赖懒加载，离线路径依赖轻）。
- 任务级 token 计量入 TaskRecord——既是观测也是预算执法依据。

### 9.5 测试与验证（"怎么证明它对"）

`go test ./...` 全绿，重点测试（面试可引用）：
- `TestHITL_FullCycle`：真实 TaskRunner 驱动 park → 票据/checkpoint 落库断言 → Deliver（带 50 次×5ms 重试竞态容忍，失败再退 Resume——与生产 API 同构）→ 终态 completed + token 计量断言。
- `TestHITL_DenyPath`：拒绝把 refusal 回喂模型、任务仍 completed、票据 decision=denied。
- `TestHITL_ResumeAfterRestart`：`hub.Shutdown()` 模拟重启 → 新 Hub 挂同一 store → Resume 从 checkpoint 续跑到 completed。
- `TestMemoryTaskStore_ConcurrentAccess` / `CRUDAndCAS` / `TTLExpiry`（Clock 注入）
- `TestSelect_KnapsackRespectsBudget` / `TestSelect_FeedbackRewards`
- `TestFingerprint_BaselinesThenBlocksDrift`（先 pin 基线后拦漂移）
- govern 全家桶：合法/缺失/坏 key、loopback 豁免、前缀边界、429+Retry-After、402、脱敏、内联 key 解析。
`make bench-router`：12 工具联邦上跑 6 条查询，打印 full/packed/saved%/recall@5/MRR 表（简历"↓72.5% / recall@5=0.92"的证据，可现场复现）。

### 9.5.1 评测子系统（v3.1 ⭐ 面试必讲）

「agent 项目没有评测系统就是玩具」——v3.1 把评测做成**门禁**而非报表：

- **双金标集**：`eval/datasets/router.v1.yaml`（100 例"查询→期望工具集"，中英 6:4，35% 多相关，含 distractor 干扰例）+ `e2e.v1.yaml`（40 例端到端：数学走真 calculator、HITL 批准/拒绝双路、脚本化工具轨迹）；
- **指标**：路由 recall@5/precision@5/MRR/hit@1/token 节省；E2E 成功率/终态/答案关键词/有序工具轨迹/意图准确率（分母只计断言了该维度的用例）；
- **确定性三层**：mock LLM/embedding（FNV-1a 位级确定）→ 评测禁 bandit（NoopFeedback）→ hybrid 排序 ToolID 决胜（消除 map 遍历序漂移）。**StableDigest**（metrics 子树 sha256 前 16 位）双跑一致可现场验证；
- **回归门禁**：`eval/thresholds.yaml`（floor 红线 + 相对基线最大跌幅 2pp）→ `make eval-gate` exit 1 → GitHub Actions `eval-gate` job 拦截合并；基线更新 = `make eval-baseline` + PR 逐指标 diff 评审；
- **评测驱动修复的 4 个真实缺陷**（讲这个最加分）：① capability/intent 词典同义词缺口（"摄氏度/下雨/带伞/save/dump"未映射）→ recall@5 0.685→0.92；② mock extractExpr 只剥前缀，"等于多少"后缀喂进 calculator 报错；③ calculator 不认中文算词（"7 减 10"）；④ hybrid 并列分受 map 遍历序影响致 digest 漂移；
- **LLMJudge 只留接口不入门禁**：`-judge llm` 接真模型评分，但禁与规则基线互比——防"裁判即考生"。

### 9.6 部署运行

```bash
cd agentcomm && make tidy && make run   # 离线免 key → http://localhost:8080
TOOLS_REQUIRE_APPROVAL=calculator make run          # HITL 演示
GOV_ALLOW_LOOPBACK=false GOV_KEYS="team-a:$HASH:tenant-a:5:10:200000|read_file,calculator|@builtin" make run   # 治理
REDIS_ADDR=127.0.0.1:6379 make run                  # 审批跨重启恢复
DISTRIBUTED=1 make run                              # registry(:8500)+math-agent(:5001)+gateway(:8080)
MCP_SERVERS=http://localhost:9000 make run          # 外部 MCP 联邦
make bench-router / make test / make grpc(-tags grpc) / make docker
```

### 9.7 ROADMAP（明确不做的——防蔓延，也是面试加分项）

MCP Tasks extension / elicitation / OAuth Resource Server(RFC 9728)、JCS 规范化签名、Agent Skills 注册表、Postgres checkpoint 后端、gRPC pb 再生成（protoc 在开发机不可用，build-tag 隔离保持默认构建不依赖）。

---

## 10. 模拟面试：面试官问答

> 使用方法：先自己口头作答，再看要点。⭐ 为高频深挖题。

### A. 开场与项目定位

**Q1 介绍一下你这个项目（2 分钟版本）**
> AgentComm 是一个位于 LLM 应用与多 Agent/MCP 工具生态之间的**治理与路由网关**。它解决企业 Agent 落地四个问题：工具多 token 贵（混合检索+背包装箱，选择开销降 70%+）、Agent 不可靠（编排层 LB+熔断+实测 EMA 反馈环）、工具不可信（WASM 沙箱+定义指纹防 rug-pull）、动作无人把关（任务持久化+HITL 审批，跨重启可恢复）。外加 virtual key/限流/预算/审计的治理层，协议对齐 MCP 2026-07 与 A2A v1.0。Go 1.25 纯 Go 单二进制，默认离线可跑。它是 C++ v1 → Go v2 → Go v3 三年迭代的产物，v1 让我懂协议，v2 让我懂架构，v3 让我懂生产。

**Q2 为什么做网关而不是直接用 LangChain/LlamaIndex 这类框架？**
> 框架解决"怎么写 Agent"，我解决"Agent 群怎么被企业管"。两者是不同层：框架在应用内，网关在应用与工具生态之间——多租户鉴权、预算、审计、审批这类横切关注点放应用里每家重写一遍，放网关里做一次全体受益。定位对标 LiteLLM/Higress 的 AI 网关面，但多了工具路由与 HITL 这两块它们没有的。

**Q3 三个版本为什么重写两次，不继续迭代？**
> v1→v2 是语言/架构级负债（C++ 假流式、无 cgo 生态、跨平台构建成本），修补成本高于重写，且核心价值在架构设计而非代码量。v2→v3 不是重写，是**增量迭代**——v2 三支柱、测试全部保留（RunAgentLoop 用 variadic LoopOpt 保持 v2 调用零改动），只加任务/治理/协议三层。我会强调重写有纪律：接口不破、测试先行。

### B. 架构与算法

**Q4 ⭐ 详细讲讲工具路由流水线，为什么是这五步、这个顺序？**
> 先检索后装箱，因为背包需要"价值"输入（融合分）；去重在反馈之后，因为去重要比较的分数应含学习信号；CandidateCap=50 在背包前，保证 O(N·budget) 良态；TopK 在背包后，因为背包优化的是价值密度，TopK 是产品层面的数量约束，两者语义不同不能合并。阈值过滤放最前是便宜先挡。

**Q5 ⭐ 为什么用 0/1 背包而不是 top-k 或按分数截断？**
> top-k 忽略体积（大 schema 工具挤爆预算），截断忽略价值。背包是"预算约束下价值最大"的精确建模，O(N·budget) 一维 DP 完全可承受（N≤50）。还有优雅降级：最差也返回单个最优工具，agent 不空手。补充：这是 0/1 不是完全背包——一个工具注入两次没有意义。

**Q6 背包分数是浮点数，DP 用 float64 有精度问题吗？**
> 理论上有，实际上分数都在 [0,1.x] 且 N≤50，累加误差远小于分数差异；若要严谨可把分数放大 1e6 取整。这是个可以主动承认的点——量化指标（token 节省%）用的是 token 整数计数，不受影响。

**Q7 混合检索为什么要 min-max 归一？BM25 和余弦直接加权不行吗？**
> 量纲不同：BM25 分数无界（正比 IDF 与 tf），余弦有界 [0,1]，直接加权 BM25 会垄断排序。归一化后各路在 [0,1] 可比。代价：对分布敏感（一个异常高分压缩其他所有值），所以还要 CandidateCap 和阈值兜底；更稳的做法可用 RRF（v2 文档里提过），v3 选了可调权重的显式融合以便做消融。

**Q8 capability 图那么简陋（关键词→类型匹配），有什么意义？**
> 它是正交信号的**最小实现**：dense 和 BM25 都基于文本相似，能力图基于"声明的 I/O 契约"，即使文本相似度失败它也能纠偏。权重只给 0.1，承认它是弱信号。这个设计表达的是架构观点：多路信号各司其职，实现可以渐进增强（未来可换成 schema 级类型推断）。

**Q9 anti-rug-pull 具体防什么攻击？怎么绕过你的防御？**
> 防"审批时看到 A 定义、运行时被换成 B"的静默漂移（description 注入 prompt、schema 放宽越权）。哈希覆盖 server_id+name+description+schema。诚实说边界：不防"pin 之前就已投毒的定义"（首次 baseline 即恶意），也不防工具**执行行为**与声明不符（那是 WASM 沙箱的职责）——两层防御互补，单层都不够。

### C. HITL 与并发（最容易被打穿的部分，务必吃透）

**Q10 ⭐⭐ 为什么审批用 park（阻塞等）而不是终止任务、批准后重新执行？**
> park 保住 LLM 上下文与已生成 token——重跑要重付全部 prompt 成本且结果不确定（LLM 非确定性）。代价是占一个阻塞的 goroutine（便宜，Go 协程 KB 级）。checkpoint 不是为了"恢复执行"，是为了**跨重启兜底**——我刻意区分 park（内存态、快）与 checkpoint-resume（持久态、慢），两档成本服务两种场景。

**Q11 ⭐⭐ 讲讲单写者原则，为什么不需要锁住 TaskRecord？**
> 每个任务记录的变更只发生在它自己的 runner goroutine：park/裁决/收尾全在 runner 线程内或经 Gate 信箱流回 runner；HTTP 侧只有 Create/Get/List（读或首次创建）。没有第二个写者就不需要写锁——并发正确性来自架构划分而不是锁。rev CAS 是给跨进程 Resume 用的兜底，不是日常路径。

**Q12 ⭐ Abort 和 Deny 为什么要区分？不区分会怎样？**
> 取消是系统行为（运维、超时、shutdown），拒绝是人的决定。混同的后果：审计链里出现"用户拒绝了 X"的假记录——合规场景下这是审计污染；且语义上游会错误地做"拒绝反馈"（把 refusal 喂回模型），而取消应该让任务安静终止。实现上 Gate.Abort 置 aborted 标志，Park 映射为 ErrAborted，不落 denied 票据。

**Q13 Deliver 和 Park 之间的竞态怎么处理？**
> 三个窗口都有防护：(1) HTTP 先查 rec.State==input_required 再 Deliver，但 waiter 可能还没注册进 map——Deliver 返回 false，掉到 Hub.Resume 慢路径，语义无损；(2) waiter 收到裁决后、写回 store 前，另一个重复 Deliver 会发现 waiter 已删——同样落 Resume，但 Resume 会检查票据已有 Decision 才重写；(3) Park 超时与 Deliver 同时——Deliver 的 select 里有 `case <-w.done` 分支，发不进不阻塞。核心思想：**快路径 miss 永远可以退到幂等的慢路径**，竞态不产生错误状态。

**Q14 重启后 task 的 runner goroutine 没了，"续跑"续的是什么？**
> 续的是 checkpoint：history 快照 + pending call。Resume 重新 spawn runner，注入 WithStartHistory(ckpt.History) + WithPending(verdict)，RunAgentLoop 开头先补记 assistant tool_call 消息、执行（或拒绝）pending call、结果回填，然后进正常循环。这保证模型看到的上下文与 park 前完全一致——**恢复语义精确到段，不是重放整段对话**。

**Q15 断线重连怎么实现"接着读"？**
> 两个层面：运行中的任务，runner 持续把答案 tee 进 TaskRecord.Answer（内存/Redis），`GET /api/tasks/{id}/events` SSE 先按差量重放 `answer[last:]` 再 300ms 轮询增量推送；已完成的任务直接 GET 全量。客户端断开只影响 tee 转发（select ctx.Done 分支继续 drain），任务照常完成。

**Q16 为什么任务 ctx 要 WithoutCancel？风险是什么？**
> 客户端断开/SSE 关闭不应杀死可能已经花了真实 LLM 成本的任务。风险：孤儿任务在无人消费时继续烧钱——所以 taskapi 的 drain 有 10 分钟超时兜底、Hub.Shutdown 会 Abort+wg.Wait 优雅收尾、Store TTL 保证记录最终过期。

**Q17 Redis 后端的 CAS 怎么做的？为什么用 Lua？**
> GET→cjson 比对 rev→rev+1→SET(PX) 必须原子，否则两个进程同时 Resume 会双双覆盖。Lua 单命令原子性天然满足；返回 -1/-2 区分 missing/conflict 让上层映射 ErrNotFound/ErrConflict。注意记录 JSON 会在 cjson 里 round-trip（时间戳变 RFC3339 字符串），所以排序一律用 Rev 不用时间。

**Q18 审批超时后任务处于什么状态？为什么不是 canceled？**
> 保持 input-required。因为"人还没决定"不等于"任务失败"——票据、checkpoint 都在，人随时可以裁决走 Resume。canceled 是终态，会丢掉继续的可能。这正是 Abort/Deny/Timeout 三者语义分离的体现。

### D. 治理层

**Q19 ⭐ 治理链的顺序为什么是 Auth → RateLimit → Budget？**
> 依赖决定顺序：限流按 key 分桶，必须先有身份；预算检查要查账（读计数器），比限流的内存操作贵，放最后让 401/429 先挡掉垃圾流量。RequestID 最外层保证后续所有日志/审计可关联。

**Q20 ⭐ 为什么明文 key 不落内存、哈希查表就够了？**
> Lookup 只需要等值匹配：sha256( presented key ) 在 map 里查即可。进程内存泄露/heap dump 时泄露的是哈希不可逆推。配合 ConstantTimeCompare 烧时间对齐 miss 路径防时序侧信道。代价：不能支持"前缀检索 key"这类明文操作——对本场景不必要。

**Q21 预算超限返回 402，为什么不是 429 或 403？**
> 语义分离：429 是"太快"（时间维度，Retry-After 可重试），402 是"花完了"（配额维度，需充值/提额），403 是"没权限"（身份维度）。AI 网关业界（LiteLLM 等）已形成这个约定，客户端可以按状态码自动化处理。

**Q22 loopback 豁免会不会成为安全漏洞？**
> 判定用 RemoteAddr 直接 peer 的 IP，**刻意不解析 X-Forwarded-For**——否则攻击者带个 XFF 头就能伪装本机。生产部署在代理后时 GOV_ALLOW_LOOPBACK=false 收紧。豁免存在只为离线 demo 零摩擦，且默认值的风险边界是"能访问到本机端口的进程"（本就拥有宿主机权限）。

**Q23 你的预算是单机 MemBudget，多实例部署怎么办？**
> 承认边界：单进程计数只够单实例语义。多实例需要共享计数器——BudgetStore 是接口，Redis INCRBY 或 Postgres upsert 都是 drop-in 实现；TaskStoreBudget 已经示范了从持久层推导用量的第二种实现。这是"接口先行、实现演进"的预留。

### E. 协议

**Q24 ⭐ MCP 三个版本你协商的规则是什么？为什么无状态 HTTP 能兼容 2026-07-28？**
> server：客户端 requestedVersion 在支持表内则回显，否则回 2025-11-25（规范：无交集回 server 最新）。2026-07-28 的方向是去 session 化（initialize 不再强制、无 Session-Id），而我的 HTTP server 本来就每请求独立、无会话状态，所以天然兼容——**不把会被规范淘汰的会话设计做进核心抽象**，这是协议演进策略。

**Q25 ⭐ A2A 卡片签名你为什么不按规范用 JCS+JWS？**
> 规范要求 JCS(RFC 8785) 规范化后 detached JWS。我签名**实际下发的原始字节**：验证者拿到的 bytes 与签名覆盖的 bytes 严格一致，防篡改保证完全等效，且不引入 canonicalization 库（JCS 的 JSON 数字/unicode 规则细节多、易实现错）。代价是与标准 verifiers 不直接互通——已文档化在 ROADMAP，属"知情取舍"。

**Q26 卡片签名密钥每次启动重新生成，重启后旧签名失效，怎么解决？**
> 确实是 demo 级简化。生产做法：从 KMS/固定文件加载长期 key，JWKS 支持 kid 轮换（新 key 上线保留旧 key 验证窗口）。这是我能主动指出的差距，修复路径清晰。

**Q27 为什么 MCP/A2A 手写而 gRPC 用标准库？**
> MCP/A2A 规范新、官方 Go SDK 成熟度低，且我需要把握手/传输/状态机的每处细节做成可教学的实现（这也是项目的学习价值）；gRPC 是成熟稳定面，手写无收益。原则：**协议自研看规范成熟度与可控性需求，基础设施自研看性价比**。

### F. 可靠性与工程

**Q28 熔断器 half-open 为什么 2 次成功才闭合？**
> 1 次成功可能是抖动/缓存命中，立刻闭合遇到真实流量雪崩重开（震荡）。2 次探测提供最小置信度；失败则立即重新 open（快速失败优于半死不活）。

**Q29 AgentStats 的 EMA 参数怎么定的？有什么坑？**
> α=0.8（新观测占 0.2）：平滑够、响应也够快；30s 截断防超时毛刺；成功率带 Beta(5,5) 先验防早期一次失败打成 0%；计数窗口 200 后 ×0.9 衰减给 agent"改过自新"的机会。坑：EMA 对突发劣化响应有滞后——所以它只影响 LB 排序（软信号），熔断（硬信号）独立存在，两者互补。

**Q30 工具结果截断为什么放 OnAfterTool 而不是工具内部？**
> 横切关注点与策略点分离：每个工具自己记得截断会遗漏第三方/WASM 工具；在 loop 的统一出口截断 = 一处策略覆盖全部执行路径。且截断后带 `meta{truncated, original_bytes}`，模型知道被截断了（比默默截断行为更好）。read_file 的 1MB 前置 stat 是补充——避免先读 100MB 再截断的浪费。

**Q31 WASM 沙箱的隔离边界在哪？wazero 相比容器沙箱呢？**
> wazero 是 runtime 级隔离：能力受限于 WASI 显式授权（无 FS/网络除非 preopen），加每次调用全新实例 + stdout 1MiB cap。它防"恶意/失控工具访问宿主"，不防侧信道、不防同权限下的逻辑滥用（比如授权只读目录里的敏感文件被读）——后者靠 READ_ROOT 收敛 + 审计。相比容器：启动微秒级、无镜像分发，但隔离强度弱于 VM 级；定位是"低信任第三方工具的默认执行档"，高敏场景应叠加容器/gVisor。

**Q32 你们怎么测试 HITL 这种跨 goroutine、跨重启的流程？**
> 三个关键手法：(1) scriptedProvider——确定性两阶段 LLM（看到 tool 结果才终结），把非确定性从测试中剥走；(2) 断言票据与 checkpoint 落库后再裁决，轮询 5s deadline 而非 sleep 固定值；(3) Deliver 带重试+幂等退 Resume——测试结构与生产竞态处理同构，测的就是真实路径。Restart 测试直接 Shutdown 旧 Hub、新 Hub 挂同一 store。

**Q33 依赖管理有什么踩坑？**
> 开发机透明加密（亿赛通）会把非白名单进程写的文件密文化，module cache 里的 cgo 源文件中招导致构建失败——用本地 replace 指向修复副本绕过（go.mod 有注释，正常环境可删）；也因此 race detector（需 cgo）本机不可用，并发正确性靠测试场景设计弥补。gRPC pb 需要 protoc，本机不可用——build-tag 隔离让默认构建/测试完全不依赖。**对不可控环境的工程适应力**也是这段经历的价值。

### G. 压力题与开放题（区分度最高）

**Q34 你的项目有什么不足？（必考，别背"追求完美"）**
> 挑真实但可控制的讲三点：① 治理计数（限流/预算）是单进程的，多实例需 Redis 化（接口已预留）；② 事件 SSE 是轮询 store 差量实现而非事件订阅，万级并发任务下改为 pub/sub 更优；③ 意图分类是关键词规则，生产应升级为小模型分类或让路由完全交给混合检索（分类器保留做快路径）。每条都要带"怎么改"。

**Q35 如果流量涨 100 倍，先改哪里？**
> 按瓶颈排序：① 内存 store → Redis（已支持）；② 嵌入计算批量+缓存已有，索引从暴力换 HNSW（VectorIndex 接口已预留）；③ 事件推送从轮询换 pub/sub；④ 单 gateway 水平扩容——这时单机治理计数必须先 Redis 化（呼应 Q23）；⑤ MCP 上游联邦加客户端缓存与熔断（Refresh 已是周期拉取，天然适配）。核心论点：**接口边界早已为扩展留缝，扩容是替换实现而非重构**。

**Q36 如果让你砍掉一半功能，留什么？**
> 留工具路由（背包+混合检索）和 HITL 双恢复路径——前者是别人没有的量化价值，后者是生产最痛的刚需。砍 gRPC 四模式、playground 美化、多策略 LB（留 LRT 一种）。判断标准：不可替代性 × 生产需求频度。

**Q37 这个项目你一个人做的，怎么证明工程协作能力？**
> 用"自我协作"证据：docs/ 三件套（ARCHITECTURE/BUILD/ROADMAP）按可交接标准写；commit 粒度按里程碑；接口先行（TaskStore/UsageSink/VectorIndex/Feedback 全是接口，演示了与"未来协作者"的边界约定）；ROADMAP 明确不做的事=对范围的纪律。

**Q38 mock LLM 会不会让你的测试没有代表性？**
> mock 是确定性的（正则识别算术→合成 tool call），测的是**管线语义**（路由/循环/审批/恢复/计量），不是模型质量——模型质量本来就不是网关的职责边界。且 provider 是接口，Qwen/OpenAI/Ollama 一键切换，真实 provider 的 SSE 解析代码在同一抽象下被同一条管线执行。

**Q39 A2A 和 MCP 都解决了 agent 互联，为什么两个都要？**
> 互补不重叠：MCP 连接"agent ↔ 工具"（能力供给），A2A 连接"agent ↔ agent"（任务委托，含任务生命周期与卡片发现）。我的网关同时是 MCP client（消费工具）+ MCP server（联邦输出工具）+ A2A server（对外暴露 super-agent）+ A2A client（调用远端 agent）——四种角色正交组合。

**Q40 下一步你会做什么（v4）？**
> 按价值排序：OAuth Resource Server（RFC 9728）替代 virtual key 域内鉴权（confused-deputy 防线）；JCS 规范化签名对齐互操作；MCP Tasks extension 映射层（等规范稳定）；Postgres checkpoint 后端支持多实例共享审批状态。展示"知道规范演进方向、按价值排队"的产品感。

### H. 快问快答（概念精确性）

**Q41 checkpoint 和 durable execution（如 Temporal）的区别？**
> checkpoint 只存"续跑所需最小快照"（history+pending call），恢复语义由 WithPending 显式续段；durable execution 是事件溯源全量重放。我只需要跨重启兜底，不值得引入重放基建。

**Q42 rev CAS 和分布式锁怎么选的？**
> 单写者原则下冲突只在跨进程 Resume 边缘出现，CAS + ErrConflict 重读足够；分布式锁引入租约/死锁/时钟问题，为一个低频路径不划算。

**Q43 为什么 select 里用 channel 而不是 sync.Cond 做审批信箱？**
> select 天然支持多路退出（裁决/超时/ctx 取消），Cond 只能 Broadcast 轮询且与 ctx 集成丑陋；channel 还能被 select 的 done 分支防住"发往已消失 waiter"的竞态。

**Q44 EstimateTokens 为什么 4 字符/token？**
> 英文经验值 ~4 chars/token，中文 1.5~2；工具 schema 以英文标识符为主取 4 偏保守。它只用于装箱与"节省%"的一致性度量（分子分母同一把尺），不需要与具体 tokenizer 对齐。

**Q45 你的 SSE 为什么 X-Accel-Buffering: no？**
> 防 nginx 反代缓冲把流式变成攒批——真流式的部署侧必要条件。

**Q46 Reserve() 再 Cancel() 的限流探测，代价是什么？**
> Reserve 先占令牌再还，窗口内有轻微"令牌空转"；换来的是能拿到精确 Retry-After 而不是粗略拒绝。限流是每 key 低频路径，代价可忽略。

**Q47 为什么任务状态机对齐 A2A 而不是自定义？**
> 网关的 Task 要通过 A2A tasks/get 直接对外投影——用同一状态机让内部记录与线格式零转换；input-required 正好承载 HITL 语义（A2A 设计里就有 human-in-the-loop 场景）。

**Q48 Beta 先验 (s+5)/(t+10) 数字的含义？**
> 相当于Beta(5,5)先验=隐含 10 次虚拟观测、先验均值 0.5——新 agent 从中性开始，10 次真实观测后实测才占主导。

**Q49 prefixMatch 为什么不能直接 strings.HasPrefix(path, p)？**
> "/api" 会命中 "/apiv2-evil"——前缀匹配必须带边界感知（等值 / p+"/" / p+"?"），govern_test 里有专门用例。

**Q50 为什么 Playground 的 decision 面板要在 token 之前发？**
> 可解释性优先：用户先看到"为什么路由到这个 agent、注入了哪些工具、省了多少 token"，再看到回答——把网关的价值决策可视化，这是演示与排查的核心 UI。

---

## 附：面试前 10 分钟速记卡

```
定位：LLM 应用 ↔ (A2A Agent + MCP 工具) 之间的治理与路由网关
三支柱：① 混合检索(0.6dense+0.3BM25+0.1cap, min-max归一) + Beta bandit + 去重 + 0/1背包(1500 token 预算, O(N·budget)) → ↓70%+
        ② 意图分类 → LB 6策略(默认LRT, EMA实测) → 熔断(closed/open/half-open, 2次闭合) → ReAct循环(5轮, LoopHooks前/后)
        ③ wazero 沙箱(零权限, 每调用新实例, stdout 1MiB cap) + 指纹pin(sha256, drift 告警/strict 阻断)
两横层：任务(submitted→working→input-required→terminal; park快路径/Resume慢路径; 单写者; WithoutCancel; Abort≠Deny)
        治理(RequestID→Auth(sha256虚拟key,401)→令牌桶(429)→预算(402)→脱敏审计; 未配置不安装)
协议：MCP 3版本协商(2025-06-18/2025-11-25/2026-07-28), A2A v1.0(supportedInterfaces 1.0+0.3, Ed25519签名+JWKS, 知情偏差:原始字节替代JCS)
量化：make bench-router 现场复现; /metrics 业务指标(router_tokens full vs packed); 任务级 token 计量→预算执法
数字卡：1500 token 预算 / TopK 5 / maxIter 5 / 截断16KB / 文件1MB / body 32KB / 熔断5次30s / EMA α=0.8 / 先验(5,5) / 审批超时1h / TTL 1h
```

**Q59：评测集怎么防止过拟合（在金标上越调越好，泛化却变差）？**
分层防：① 数据集分 easy/hard/distractor 三档，distractor 专门放"表面词干扰"（如"搜索到的资料写成文件"——真意图是写文件不是搜索），调词典时容易顾此失彼，天然抑制死记硬背；② 保持 holdout 习惯：新同义词先在人工抽样上验证再加词典；③ 门禁同时看 recall 和 precision——盲目扩词典会拉低 precision@5，被跌幅门禁拦住；④ 词典只做"能力信号"（0/1），排序主力是 dense+BM25，不存在无限堆关键词的通道。

**Q60：为什么不用 ragas/deepeval 这类现成评测框架？**
三个理由：① 本项目的评测对象是"路由选择 + 任务状态机"（结构化、确定性），不是自由文本生成质量——rule-based 指标就是对的度量，ragas 的 faithfulness/answer_relevancy 反而不对口；② 零 Python 依赖：评测要进 CI 门禁和单二进制交付，引 Python 运行时破坏"纯 Go 离线可复现"的卖点；③ 可控性：StableDigest 字节级稳定、阈值门禁、基线 PR diff 这些门禁语义，自己 400 行就能精确实现。LLM-as-judge 留了适配器接口（LLMJudge），真要评生成质量时接得上。
