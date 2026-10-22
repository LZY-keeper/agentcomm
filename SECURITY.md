# 安全政策

agentcomm 是一个治理与安全导向的 Agent 网关：工具指纹锁定、WASM 沙箱、审计脱敏、
HITL 审批都是它的核心承诺。安全问题的报告渠道因此格外重要——请花一分钟读完本文件。

## 报告安全漏洞

**请不要在公开 issue 中描述可利用的安全问题。**

- 邮件：通过 GitHub 账户资料中的联系邮箱私下联系维护者；或
- 使用 GitHub 的 [私下漏洞报告](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability) 功能（仓库 Security 页签 → Report a vulnerability）。

我们会在 72 小时内确认收到，并在修复发布前与你同步进展。

## 范围说明

值得报告的例子（非穷尽）：

- 工具指纹锁定（anti-rug-pull）可被绕过的路径；
- 治理层（virtual key / 限流 / 预算）的鉴权或计费绕过；
- 审计日志或审批 webhook 中的敏感信息泄漏（含脱敏遗漏）；
- WASM 沙箱逃逸或权限提升；
- HITL 审批流的状态机缺陷（如绕过 park、伪造裁决）；
- MCP / A2A 协议实现的请求伪造或重放问题。

**不在范围内**：针对默认离线 demo 形态的"未开治理"告警（`GOV_KEYS` 未配置时
治理层整体不安装，这是文档化的设计取舍而非漏洞）；需要宿主机已有控制权的攻击。

## 设计取舍声明

以下行为是**文档化的有意设计**，重复报告前请先对照文档：

- `GOV_ALLOW_LOOPBACK=true`（默认）：本机回环流量豁免鉴权，仅为离线 demo 零摩擦；
  生产部署必须置 `false`（`X-Forwarded-For` 刻意不参与判定，防止头伪造绕过）。
- virtual key 只存 SHA-256 哈希：明文 key 不落进程内存；代价是不支持前缀检索。
- 审批 webhook 只外发脱敏后的参数摘要；未配置 URL 时功能整体关闭。

## 密钥与凭证纪律

- 任何凭证、令牌、授权文件不得提交进仓库或 Git 可跟踪路径；
- `GOV_KEYS` / `APPROVAL_WEBHOOK_SECRET` 等建议经环境变量或部署密管注入；
- 若发现提交中含敏感信息，请立即通过上述渠道报告（这本身按漏洞处理）。
