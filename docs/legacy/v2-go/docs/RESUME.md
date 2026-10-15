# 项目经历 · 企业级智能 Agent 网关（MCP 工具路由引擎）

> 本文档为简历用项目描述（对应 `jianli/` 原版格式，按最终形态重写）。建议在面试中作为「主项目」讲述。

**项目名称**：AgentComm —— 企业级智能 Agent 网关 / token 预算感知的 MCP 工具路由引擎
**时间 / 角色**：2025-05 ~ 2026-07 ｜ 独立设计开发（C++ v1 原创实现 → Go v2 架构重写与升级）
**技术栈**：Go 1.22（v2）、C++17/gRPC（v1）；MCP（2025-06-18）、A2A（2025）官方规范；wazero(WASM)、OpenTelemetry、Prometheus、gRPC 四模式

## 项目背景与定位

随着 MCP（Model Context Protocol）与 A2A（Agent2Agent）在 2025 成为事实标准，企业 AI 落地出现一个**尖锐痛点**：每接入一个 MCP server，其全部工具 schema 都会被**全量注入** LLM 上下文——**50+ 工具每次请求就吃掉 5K+ token**，成本飙升、工具选择准确率下降；同时各业务系统各自接 LLM/Agent，碎片化、缺治理、跨 Agent 协作困难、第三方工具裸跑有安全风险。

本项目不做「又一个 Agent 框架」，而是定位为**企业 AI 网关 / 工具路由引擎**——位于「LLM 应用」与「多 Agent + 海量 MCP 工具」之间的治理层，对上暴露一个「超级 Agent」单一入口，对下用三项核心创新把分散能力整合为**低成本、高可靠、可治理**的统一中台。

## 核心创新（三大技术支柱）

### 1. 语义 + token 预算感知的联邦 MCP 工具路由引擎（核心算法）
超越市面「embed 工具 → top-k 余弦」的朴素做法：
- **联邦发现**：聚合 N 个 MCP server 的全部工具到统一能力注册表；
- **混合检索 + 融合排序**：稠密向量（语义）⊕ BM25（精确词）⊕ 能力图（I/O 类型兼容），min-max 归一化后加权融合——三种正交信号互补，召回与精度均优于单一稠密；
- **🌟 token 预算感知装箱**：把工具 schema 体积视为「重量」、检索价值视为「价值」，在 LLM token 预算内做 **0/1 背包 DP**，选出**价值最优且不超预算**的工具子集——既非忽略体积的朴素 top-k，也非忽略价值的截断，**直接把工具选择 token 占用降低 70%+**；
- 跨 server 语义去重 + 多臂老虎机（Beta bandit）反馈闭环，按任务成败持续学习重排。

### 2. 成本与可靠性感知的多 Agent 编排
- A2A 编排：意图识别 → 注册中心服务发现（tag 索引 + 心跳）→ 负载均衡（6 策略：轮询/随机/最少连接/加权轮询/一致性哈希/最短响应时间）→ 熔断器（closed/open/half-open 真状态机）→ 等价降级；
- **真·agentic 工具循环**（ReAct 风格）：注入背包选出的工具 → LLM 流式；模型请求工具调用时执行→回填→再提示，直到完成；
- **端到端真 token 流式**（channel 贯穿 LLM→编排→A2A SSE→gRPC stream→Playground），替换了 v1 的「切片+sleep」假流式。

### 3. WASM 沙箱工具运行时（安全 + 跨平台）
- 第三方/不可信工具编译为 `.wasm`，用 **wazero**（纯 Go、无 cgo）沙箱执行，**跨平台单二进制**，彻底替换 v1 中 Windows(LoadLibrary) 与 POSIX(fork) 互锁死、无平台能跑通的插件加载；
- **运行时级沙箱 + WASI 权限**：默认无文件/网络访问，显式授权才放开——直接回应 MCP 第三方工具的供应链/越权风险（市面大多裸跑）。

## 主要功能模块

**1. RPC 通信（gRPC 真四模式）**：AIQueryService（unary Query、server-stream QueryStream 真 token 流）；AgentCommunicationService 的 RealTimeCommunication（**bidi 真跨客户端路由**，修复 v1 的回声桩）、BatchSendMessages（client-stream）、ListenMessages（server-stream）。

**2. A2A 协议（2025 官方规范）**：Agent Card（`/.well-known/agent-card.json`）+ `message/send`（同步）+ `message/stream`（SSE 流）；任务状态机对齐规范（working/input-required/…）；注册中心（tag 索引 + 心跳 + 健康清理）；远端 agent 经适配器与本地 agent 统一编排。

**3. MCP 协议（2025-06-18 官方规范）**：自实现 `initialize` 握手 → `notifications/initialized` → `tools/list` / `tools/call`，stdio + Streamable HTTP 双传输（修复 v1「从不发 initialize、SSE 端点对不上」的硬伤）；既可作为 server 暴露工具，也可作为 client 联邦外部 MCP server。

**4. RAG-MCP 智能工具选择**：见创新 1（混合检索 + token 背包装箱 + 反馈），是网关的「智能路由」核心。

**5. 可观测性 / 工程**：Prometheus `/metrics`（含**工具选择 token 节省量、检索召回、成本、熔断状态**等业务指标）+ OpenTelemetry 全链路追踪 + 结构化日志；Web Playground 可视化路由决策（命中 agent / top-k 工具及分数 / token 装箱对比 / 工具调用 / 延时 / trace）；**默认离线 mock provider 免 API key 可跑全链路**，真实 Qwen/OpenAI/Ollama/DashScope 可插拔。

## 量化成果（系统内置测量，可现场演示）

- 工具选择 token 占用 **↓ 70%+**（`make bench-router` 输出全量 vs 背包装箱对比）；
- 混合检索在构造工具集上召回/精度优于纯稠密；
- 单查询成本/延时/P50 首 token 经 `/metrics` 可观测；
- 等价降级（熔断 + fallback）提升整体可用性。

## 难点与亮点（面试深挖点）

- **为什么是背包而不是 top-k**：top-k 忽略工具体积，大 schema 会撑爆预算；截断忽略价值；0/1 背包是「体积约束下最大化价值」的精确解，O(N·预算) 完全可行。
- **混合检索的三路融合**：稠密管语义、BM25 管精确标识符/术语、能力图管类型兼容——归一化后加权，规避了原始分尺度不一的问题。
- **真流式 vs 假流式**：v1 是 LLM 一次性出全文再切片+sleep 模拟；v2 用 channel 把 LLM 的 token 逐个推到 UI，端到端真实。
- **WASM 沙箱的价值**：跨平台（一份 `.wasm` 到处跑）+ 安全（默认零权限）一举解决 v1 的两大死结。
- **协议合规**：手写实现并严格对齐官方 MCP/A2A 规范（握手、传输、状态机），比「调一个 SDK」更能体现协议掌握深度。

## 双版本演进叙事（简历主线）

> 先用 **C++17 + gRPC** 从零原创实现了完整通信框架（gRPC 四模式、A2A、MCP、RAG-MCP、多 Agent），跑通主链路与阿里百炼 Qwen 集成；在复盘其**工程局限**（手搓协议的合规偏差、POSIX/Win32 跨平台断裂、假流式/假异步、指标无端点）后，**用 Go + 官方规范 + WASM 沙箱 + 可观测性重写为生产级网关 v2**，并新增 token 预算背包装箱这一核心算法创新。体现了「能从零造系统」与「能用现代工程方法重构升级」两种能力的结合。

---

### 附：目录与运行

- 代码：`go-v2/`（v2 Go 实现，可运行）；`a2a/ mcp/ common/ server/ client/ orchestrator/ ...`（v1 C++ 原作，保留）。
- 运行：`cd go-v2 && make tidy && make run` → http://localhost:8080（离线免 key 演示）。详见 `docs/BUILD.md`。
