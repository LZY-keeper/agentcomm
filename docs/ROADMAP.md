# ROADMAP（v3 明确不做的事）

> 按「阶段5」方法论，明确非目标是防蔓延。以下按价值排序，供 v4 参考。

1. **MCP Tasks extension**（SEP-2663）：服务端长任务原语；当前 HITL 用自有
   TaskStore + A2A Task 覆盖了同一需求，待 extension 稳定后可做映射层。
2. **MCP elicitation / URL mode**：服务端向用户带内/带外收集输入。
3. **OAuth Resource Server（RFC 9728）**：`/.well-known/oauth-protected-resource`
   元数据端点 + audience binding（confused-deputy 防线）；当前以 virtual key
   域内鉴权替代。
4. **JCS 规范化卡片签名**：当前以"签名原始字节 + jwks 公钥"等效实现；规范
   兼容需 RFC 8785 canonicalization。
5. **Agent Skills 注册表**（agentskills.io SKILL.md）：技能包 → 工具集映射
   与 progressive disclosure。
6. **Postgres checkpoint 后端**：跨实例共享任务状态（当前 Redis 单实例即可恢复）。
7. **gRPC 四模式 pb 再生成**：protoc 工具链在本开发机不可用；build-tag 隔离
   保持默认构建不依赖。

## 评测子系统明确不做的事（v3.1）

8. **分布式/压测评测**：单进程确定性评测已覆盖质量回归；吞吐/容量属另一套基准设施。
9. **人工标注平台**：金标集由开发者维护（YAML diff 即评审面），140 例规模不值得引平台。
10. **线上反馈闭环（online eval）**：生产流量采样评分需要真实流量与隐私评审，当前以离线金标 + 门禁达成回归目标。
11. **全自动 LLM-as-judge 生产门禁**：LLMJudge 仅留适配接口（`-judge llm`，禁入门禁）——LLM 评分不与规则基线互比，防"裁判即考生"。
12. **A/B 多环境对比报表**：单基线 + 阈值已满足回归语义；多臂对比等真实多环境出现再做。
