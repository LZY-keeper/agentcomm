# eval/reports/ 目录约定

评测报告的版本化证据链目录（证据链 = 可复现 + 可审计 + 可回滚）：

```
reports/
├── README.md      # 本文件
├── baseline/      # 回归门禁基线（提交进库；PR diff 即评审面）
│   └── report.json
├── latest/        # 最近一次运行的报告副本（CI/部署态 playground 读取）
│   ├── report.json
│   └── report.md
└── <yyyymmdd-hhmmss>-<commit8>/   # 历史运行（gitignore，不入库）
```

## 命令

| 命令 | 作用 |
|---|---|
| `make eval` | 本地全量评测，写时间戳目录并复制 `latest/` |
| `make eval-gate` | 对比 `baseline/report.json` 回归门禁（失败 exit 1，CI 同款） |
| `make eval-baseline` | 重建基线——先跑 `make eval` 确认指标合理，再提交 diff |

## 基线更新流程（评审面）

1. 改动影响指标（路由参数/数据集/权重/分词等）→ `make eval` 查看新指标；
2. 指标提升或可接受的权衡 → `make eval-baseline` 重建基线；
3. `git diff eval/reports/baseline/report.json` —— 逐指标 diff 进 PR 描述；
4. CI 的 `eval-gate` job 在 PR 上强制执行：任何未过评审的指标回退都会挂门禁。

## 可复现性

- 报告含 `StableDigest`（仅 metrics 子树的 sha256 前 16 位）：同配置重跑同 digest。
- 门禁只比较 metrics 子树；时间戳/延迟不参与。
- 全离线：mock LLM + mock embedding（确定性 FNV-1a），不依赖网络与 API key。
