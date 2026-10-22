# 贡献指南

agentcomm 欢迎 issue 与 PR。本仓库是单人可维护的规模，工程纪律集中在**两件事**：
改对地方之前先读对应文档；提交之前过评测门禁。

## 触发式阅读路由（改哪块代码，先读哪份文档）

| 你要动的东西 | 必读 | 关键不变量 |
|---|---|---|
| `internal/router/`（检索/背包/指纹/反馈） | `docs/ARCHITECTURE.md` 支柱一 | 评测路径必须保持 `NoopFeedback` 无状态——金标确定性是门禁红线 |
| `internal/eval/`、`eval/datasets/`、`eval/thresholds.yaml` | `docs/ROADMAP.md` 评测节 | 指标回退超阈值即 CI 红；重建基线必须单独 PR 并附逐指标 diff 说明 |
| `internal/orchestrator/task.go`（HITL/审批） | `docs/ARCHITECTURE.md` 任务横层 | 单写者原则：TaskRecord 变更只发生在 runner goroutine；Abort ≠ Deny；审批超时保持 input-required |
| `internal/govern/`（key/限流/预算/审计） | `docs/ARCHITECTURE.md` 治理横层 | 脱敏链：任何新增出站路径只允许携带 `maskSecrets` 后的字段 |
| `internal/notify/`（审批触达） | `SECURITY.md` | fire-and-forget：投递失败不得阻塞/失败审批主流程 |
| `internal/mcp/`、`internal/a2a/`（协议） | `docs/ARCHITECTURE.md` 协议节 | 三版本协商与 A2A v1.0 签名语义不做静默变更 |
| 面向用户的 README / docs | 现有文档 | README 的承诺即契约——新增能力先能跑通再写进去 |

## 提交前检查

```bash
make test        # 全部单测
make vet         # go vet
make eval-gate   # 评测回归门禁（改过路由/编排/评测必跑）
```

- 提交信息用中文或英文均可，一行主题说清"为什么"；
- 评测基线更新（`make eval-baseline`）与功能改动分开提交；
- 行为变化同步更新 `docs/BUILD.md` 的环境变量表与 README 能力矩阵。

## 范围纪律

`docs/ROADMAP.md` 的「明确不做」清单是有意的范围控制。提案新能力前先看一眼——
如果它在那份清单上，请先开 issue 讨论边界，再动手实现。
