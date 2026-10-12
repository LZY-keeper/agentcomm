# 项目经历 · 企业级智能 Agent 网关 v3（MCP 工具路由 + HITL + 治理层）

> 本文档为简历用项目描述。建议在面试中作为「主项目」讲述。

**项目名称**：AgentComm —— 企业级智能 Agent 网关（工具路由 · 任务持久化 · 人工审批 · 治理层）
**时间 / 角色**：2025-05 ~ 2026-09 ｜ 独立设计开发（C++ v1 原创 → Go v2 架构重写 → Go v3 企业化迭代）
**技术栈**：Go 1.25（纯 Go 无 cgo）；MCP（2025-06-18 → 2026-07-28 三版本协商）、A2A v1.0（Ed25519 卡片签名）；wazero(WASM)、OpenTelemetry、Prometheus、gRPC 四模式、Redis、x/time 令牌桶

## 项目背景与定位

MCP/A2A 成为事实标准后，企业 AI 落地的痛点不止"工具多 token 贵"：**生产级 agent 还缺三样东西——敏感动作的人工审批闭环、调用方治理（谁在用/花多少/能调什么）、以及协议快速演进下的合规能力**。本项目是位于「LLM 应用」与「多 Agent + 海量 MCP 工具」之间的治理与路由层，v3 在 v2 三支柱（混合检索+token 背包工具路由 / 多 Agent 编排 / WASM 沙箱）之上补齐了这三块，达到可直接上线交付的形态。

## v3 核心增量

### 1. 任务持久化 + 人工审批 HITL（`internal/store` + `internal/approval` + orchestrator/task.go）
- 任务状态机对齐 A2A（submitted→working→input-required→…）；工具注册标记 `RequireApproval`（环境变量可批量强制）；ReAct 循环命中即 **park**：checkpoint（历史+pending call）落库 → SSE 推 `input_required` 事件 → 等待人工裁决；
- **双恢复路径**：进程内 Gate 信箱快路径（裁决零延迟续跑）；无 waiter（重启后）自动落到 checkpoint 续跑慢路径，Redis 后端使审批**跨重启可恢复**（启动时 RecoverPending 重挂）；
- **断线不停任务**：任务 ctx 与 HTTP 请求解耦（WithoutCancel），tee 转发 + 全量答案持久化，客户端可随时重连续读；
- 并发设计亮点：**单写者原则**（任务记录只在 runner goroutine 变更，rev CAS 兜底跨进程）；Abort≠Deny（取消不产生"拒绝"票据）；Gate 的 select 四路退出零泄漏。

### 2. 企业治理层（`internal/govern`）
以 2026 AI 网关企业基线（LiteLLM/Higress 开源面）为对标：**virtual key**（sha256 内存态、tenant、每 key 限流+预算+工具白名单）→ Auth(401) → 令牌桶限流(429+Retry-After) → token 预算(402) → 全量脱敏审计日志；`govern.Chain` 包装原 mux 零路由侵入；未配置 key 时整层不安装，离线 demo 零摩擦。

### 3. 协议对齐 2026 + 供应链安全
- **MCP**：server 端三版本协商（2025-06-18/2025-11-25/2026-07-28），无状态 HTTP 天然兼容 2026-07-28 的"去 session 化"方向；client 端 2025-11-25 发起+回退；网关新增 `/mcp` 对外暴露工具；
- **A2A v1.0**：卡片 `supportedInterfaces`（1.0+0.3 兼容声明）、extended card RPC、`message/send` 返回规范 Task、`tasks/get|cancel`；卡片 **Ed25519 签名**（`X-AgentCard-Signature` + JWKS 公钥端点，防卡片伪造/中间人篡改）；
- **anti-rug-pull**：工具定义指纹 pin，上游静默改定义（tool poisoning/rug pull 经典向量）即 ERROR 审计 + strict 模式阻断入索引。

### 4. 可靠性/安全修复（v2 → v3）
- agent 延迟/成功率 EMA 实测反馈环，LB 的 least_response_time 真实数据驱动；
- 工具结果 16KB 截断（防上下文爆炸）、read_file 1MB 上限、请求体 32KB 上限；
- 审计参数脱敏（password/token/secret/api_key）、loopback 豁免可关且不信任 XFF；
- 依赖 API 漂移修复（wazero 模块路径、OTel SDK），默认离线 mock 全链路可跑。

## 量化亮点（v3.1 评测子系统背书，`make eval` 可现场复现）
- 工具选择 token 占用 **↓72.5%**（0/1 背包装箱 vs 全量注入，100 例金标均值）；
- 混合检索质量：**recall@5 = 0.92 / MRR = 0.73**（100 例中英双语金标，含 distractor 干扰例）；
- E2E 任务成功率 **100%**（40 例金标，覆盖 HITL 审批批准/拒绝双路）；
- **GitHub Actions 评测回归门禁**：每次提交对版本化基线强制复验，指标回退超 2pp 即 fail；报告含 StableDigest（双跑一致性可验证）；
- 评测驱动修复 4 个真实缺陷（capability/intent 词典缺口、calculator 中文算词、hybrid 并列分不确定性）——"评测不是报表，是抓 bug 的";
- Prometheus 业务指标 + OTel 追踪 + 任务级 token 计量（预算执法依据）。

## 面试深挖点（v3 新增）
- **为什么 park 而不是终止重跑**：park 保住 LLM 上下文（零重付成本），checkpoint 只为跨重启兜底——"checkpoint ≠ durable execution"，恢复语义由 WithPending 精确续段保证；
- **单写者原则**：并发正确性靠架构（谁写谁负责）而非锁堆积；
- **Abort 与 Deny 为何要区分**：取消是系统行为、拒绝是人的决定，混同会污染审批审计链；
- **治理层为何默认关闭**：企业功能与开发者体验不冲突——零配置离线可跑是采用率的前提，GOV_* 一开即生产形态；
- **协议栈策略**：跟进 2026-07-28 无状态化方向（网关任务态自有、HTTP 层无会话），不把会被规范淘汰的设计做进核心抽象。

## 三版本演进叙事（简历主线）
C++ v1 从零实现通信框架 → 复盘工程局限（假流式/跨平台/无指标）后 Go v2 生产级重写（真流式、WASM 沙箱、背包算法创新）→ v3 站在生产上线视角补齐 HITL 闭环、治理层与 2026 协议合规。体现"能造系统 → 能重构系统 → 能交付系统"的完整能力闭环。

---
### 附：目录与运行
- 代码：`go-v3/`（v3，可运行）；`go-v2/`（v2 档案）；根目录（v1 C++ 原作）
- 运行：`cd go-v3 && make tidy && make run` → http://localhost:8080（离线免 key）
- HITL 演示：`TOOLS_REQUIRE_APPROVAL=calculator make run`
- 详见 `docs/BUILD.md`、`docs/ARCHITECTURE.md`、`docs/ROADMAP.md`
