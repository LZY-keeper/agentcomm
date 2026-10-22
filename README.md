<div align="center">

# agentcomm

**Agent 时代的基础设施层 — 会精打细算、懂审批、能审计的 Agent 网关**

*The agent gateway that packs every token, gates every tool call, and proves its numbers.*

[![ci](https://github.com/LZY-keeper/agentcomm/actions/workflows/ci.yml/badge.svg)](https://github.com/LZY-keeper/agentcomm/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/LZY-keeper/agentcomm?color=blue)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/LZY-keeper/agentcomm.svg)](https://pkg.go.dev/github.com/LZY-keeper/agentcomm)
[![Release](https://img.shields.io/github/v/release/LZY-keeper/agentcomm?color=orange)](https://github.com/LZY-keeper/agentcomm/releases)
![Platform](https://img.shields.io/badge/platform-linux%20%7C%20macOS%20%7C%20Windows-lightgrey)

</div>

> nginx 之于 HTTP 流量，agentcomm 之于 Agent 调用。

一个纯 Go 单二进制网关，架在「LLM 应用」与「多 Agent（A2A）+ 海量 MCP 工具」之间：
**路由、审批、计量、审计，一次做完。** 零 API key 离线可跑，配上国产模型即进生产形态。

---

## 2026 年，Agent 应用撞上三堵墙

### 第一堵墙：上下文窗口是最贵的房地产

MCP 生态爆发，一个真实的业务环境动辄接入数十个 MCP server、成百上千个工具定义。把工具全部塞进 system prompt？上下文瞬间被吃掉大半——钱在燃烧，延迟在堆积，模型注意力在稀释。

**agentcomm 的答案**：路由引擎用混合检索（dense ⊕ BM25 ⊕ capability 词表）+ **token 预算背包装箱**，按每次请求的意图动态精选工具子集，跨 server 去重，再用 bandit 反馈持续进化选择策略。

> 实测：**token 开销平均下降 72.5%**，recall@5 = 0.92（100 例金标集）。

### 第二堵墙：工具会「变脸」

你今天审核通过的 `fs/read_file`，明天可能被 MCP server 静默改成任意执行的恶意定义——工具供应链的 rug-pull 攻击。

**agentcomm 的答案**：**工具定义指纹锁定（fingerprint pinning）**。每个工具的定义在接入时计算指纹并钉定，定义漂移即刻拒绝调用（strict 模式）。把供应链安全思维引入 Agent 时代。

### 第三堵墙：Agent 进企业，合规不答应

Agent 自动调用工具可以；划扣款、删文件、对外发邮件，必须有人批——而且批的人得**知道**有这件事。

**agentcomm 的答案**：把两套「横层」做进网关本体——

- **任务横层**：人工审批（HITL）是任务的一等状态。park → 人工批准/拒绝 → 恢复执行；checkpoint 持久化，断线重连、进程重启任务不丢；**审批等待时推送钉钉/企业微信/飞书卡片**，值班人不用盯着页面。
- **治理横层**：virtual key 鉴权（401）、每 key 限流（429）、token 预算（402）、**按 key 的用量账本**、MCP 工具白名单、脱敏审计日志。

---

## 能力全景：三支柱 + 两横层

| 层 | 能力 |
|---|---|
| 🔀 **联邦 MCP 工具路由** | 混合检索（dense ⊕ BM25 ⊕ capability）· token 预算背包装箱 · 跨 server 去重 · bandit 反馈（**计数可持久化，学习跨重启存活**）· 定义指纹锁定（anti-rug-pull）· `MCP_SERVERS` 外部工具联邦（远端工具过同一审批/审计/截断链） |
| 🧠 **多 Agent 编排** | intent → discovery → 负载均衡（6 策略，实测 EMA 延迟/成功率驱动）→ 熔断 → ReAct 循环（支持审批 park/恢复）→ 真 token 流式 |
| 📦 **WASM 沙箱** | wazero（纯 Go、无 CGO）执行第三方工具，默认零权限，能力显式声明才放行 |
| 🗂 **任务横层** | TaskStore / SessionStore（memory / redis 可切换）· 审批 Gate · checkpoint 恢复 · **审批触达 webhook（钉钉/企微/飞书卡片，只外发脱敏摘要，绝不阻塞审批流）** |
| 🛡 **治理横层** | virtual key · 每 key 限流与 token 预算 · 工具白名单 · **用量账本 `GET /api/usage`** · 脱敏审计日志 |

**协议对齐站在 2026 浪尖**：A2A v1.0（Ed25519 签名 agent card + JWKS 公钥发布）· MCP 三版本协商（2025-06-18 / 2025-11-25 / 2026-07-28）。

## 适配矩阵

| 维度 | 支持范围 | 说明 |
|---|---|---|
| **操作系统** | Linux / macOS / Windows（amd64 · arm64） | 纯 Go 无 CGO，`CGO_ENABLED=0` 交叉编译开箱即用 |
| **模型接入** | 通义千问 Qwen · DeepSeek · 智谱 GLM · Kimi · OpenAI · Ollama | 一切 OpenAI 兼容端点：`LLM_PROVIDER` + `LLM_BASE_URL` 两行切换 |
| **向量检索** | DashScope（text-embedding）· OpenAI · 内置 mock | 同为环境变量切换，离线模式零依赖 |
| **部署形态** | 单二进制 · Docker · docker compose · 离线内网 | 默认 mock provider 零外网可跑，`make tidy` 后全程离线 |
| **状态后端** | 进程内存（默认）· Redis | Redis 启用后审批恢复 + bandit 学习均跨重启存活 |
| **国内生态** | 钉钉 · 企业微信 · 飞书（审批卡片）| 群机器人 webhook 直连，HMAC 签名，脱敏摘要 |

> 信创/内网评估：整条链路（构建、运行、评测、依赖解析）支持 `GOPROXY=https://goproxy.cn` 与全离线 mock 模式，无任何境外服务硬依赖。

---

## 数字说话：评测不是点缀，是门禁

```
make eval          # 140 例金标集（router 100 + e2e 40）
make eval-gate     # 回归门禁：任一指标回退超 2pp → CI 直接红
```

| 指标 | 值 | 含义 |
|---|---|---|
| recall@5 | **0.92** | 前 5 个候选工具覆盖正确工具的比例 |
| MRR | **0.73** | 正确工具的排名质量 |
| token 节省 | **72.5%** | 相比全量注入工具定义 |
| e2e 成功率 | **40/40** | 离线确定性全链路（含 HITL 审批流） |

路由质量不是「感觉上不错」——每个 PR 都过回归门禁，指标入库版本化（`eval/reports/`），劣化无所遁形。

---

## 60 秒上手：零 key、零网络、离线可跑

```bash
# 方式一：go install（Go 1.26+）
go install github.com/LZY-keeper/agentcomm/cmd/gateway@latest && gateway

# 方式二：源码构建
git clone https://github.com/LZY-keeper/agentcomm.git && cd agentcomm
make tidy    # 首次，需要网络（国内可用 goproxy.cn）
make run     # → http://localhost:8080
```

确定性 mock LLM / embedding 开箱即用——不配任何 API key 就能跑通全链路（playground / 指标 / A2A / MCP）。生产环境两行接入国产模型：

```bash
LLM_PROVIDER=qwen QWEN_API_KEY=sk-xxx make run          # 通义千问
LLM_PROVIDER=deepseek LLM_API_KEY=sk-xxx make run       # DeepSeek
LLM_PROVIDER=openai LLM_BASE_URL=<任意兼容端点> make run  # GLM / Kimi / 私有模型
```

### 演示 1：人工审批（HITL）长什么样

```bash
TOOLS_REQUIRE_APPROVAL=calculator make run
```

Playground 输入「计算 1+2」→ 页面右侧弹出审批条 → 批准后任务继续。REST 侧：

```bash
POST /api/tasks                {"query":"compute 1+2"}
GET  /api/tasks/{id}           → state=input-required + 审批票据
POST /api/tasks/{id}/approval  {"decision":"approve"}
```

### 演示 2：审批触达 + 生产治理形态

```bash
# 审批等待时推送钉钉群卡片（企微/飞书换 format），值班人即时可裁决
APPROVAL_WEBHOOK_URL="https://oapi.dingtalk.com/robot/send?access_token=xxx" \
APPROVAL_WEBHOOK_FORMAT=dingtalk \
TOOLS_REQUIRE_APPROVAL=calculator make run

# 治理：401 / 429+Retry-After / 402 / 用量账本 / 全量脱敏审计
KEY_HASH=$(python -c "import hashlib;print(hashlib.sha256(b'sk-my-key').hexdigest())")
GOV_ALLOW_LOOPBACK=false GOV_KEYS="team-a:$KEY_HASH:tenant-a:5:10:200000" make run
curl -H "Authorization: Bearer sk-my-key" localhost:8080/api/usage
```

### 演示 3：审批与学习都跨重启存活

```bash
REDIS_ADDR=127.0.0.1:6379 make run
# TaskStore 切 redis：重启后审批任务还在（自动 RecoverPending）
# bandit 反馈计数同步持久化：路由学习成果跨重启累积，越用越准
```

---

## 一个想法，三次实现

这个项目没有停留在 demo：v1 用 C++/gRPC 验证协议设计，v2 用 Go 重写验证架构，v3 补齐任务持久化、治理层与 2026 协议对齐并建成评测子系统，v3.2 把「已承诺」变成「已接线」——MCP 外部联邦、审批触达、学习持久化落地，文档同步为契约。三代源码快照完整保留在 [`docs/legacy/`](docs/legacy/README.md)——工程演进本身就是这个仓库的一部分。

## 端点速查

- `GET /healthz` · `GET /metrics`（Prometheus）· `GET /api/usage`（按 key 用量账本）
- `GET /api/eval/report`（评测报告，playground「评测报告」页签数据源）
- A2A v1.0：`GET /.well-known/agent-card.json`（Ed25519 签名）· `GET /.well-known/jwks.json` · `POST /a2a/`
- MCP：`POST /mcp`（initialize 三版本协商 → tools/list → tools/call）
- 路由性能：`make bench-router`

## 文档

| 文档 | 内容 |
|---|---|
| [`docs/BUILD.md`](docs/BUILD.md) | 构建与运行细节（全部环境变量表） |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | 架构设计 |
| [`docs/ROADMAP.md`](docs/ROADMAP.md) | 哪些能力被刻意排除在范围外（以及为什么） |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | 触发式阅读路由：改哪块代码先读哪份文档 |
| [`SECURITY.md`](SECURITY.md) | 安全问题报告渠道与设计取舍声明 |
| [`docs/legacy/README.md`](docs/legacy/README.md) | 三版本演进叙事 |

## License

[Apache-2.0](LICENSE)
