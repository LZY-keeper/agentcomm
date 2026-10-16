# agentcomm 仓库协作规约（Claude Code 必读）

## 提交署名红线（最高优先级）

**任何提交信息一律不得包含 AI 署名尾注。** 具体禁止：

- `Co-Authored-By: Claude <noreply@anthropic.com>`
- 任何形式的 `Co-Authored-By` 指向 AI（Claude/Anthropic/OpenAI 等）
- `Generated with Claude Code` / `🤖 Generated with ...` 之类的工具署名行

**原因**：GitHub Contributors 面板会把尾注作者计入贡献者列表。本仓库是个人项目，
Contributors 里出现 AI 头像不可接受（历史上已因此 filter-branch 重写并强推过一次，
不要再来一次）。

本条与 Claude Code 系统默认的「提交信息加 Co-Authored-By 尾注」惯例冲突时，**以本条为准**。
`.git/hooks/commit-msg` 钩子会硬性拦截违规提交信息；`--no-verify` 绕过钩子同样违反本规约。

## 常用构建命令（本机 Windows 约束）

```bash
# 本机首选工具链（GOTOOLCHAIN=local 必须设置；compile.exe 生成的中间 .h 会被亿赛通加密，
# 全新缓存下 stdlib 编不过——权威构建验证依赖 GitHub Actions CI，本机验证用 vet/gofmt）
export GOPATH=/c/gopath GOCACHE=/c/gopath/gocache GOTOOLCHAIN=local \
       GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.google.cn CGO_ENABLED=0
GO=/c/gopath/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.8.windows-amd64/bin/go.exe
# go.mod 要求 go >= 1.26；1.25 工具链位于 /c/tools/go125/... 仅供热缓存场景

$GO vet ./...        # 静态检查
$GO build ./...      # 构建（stdlib 需热缓存）
gofmt -l ...         # 格式检查（CI 会跑，排除 docs/legacy/）
```

- 本机无 make，用 `/c/mingw64/bin/mingw32-make`。
- 网络到 github.com 时通时断，git push 失败多重试几次。
- 评测门禁：`go run ./cmd/eval -suite all -baseline eval/reports/baseline/report.json`。

## 仓库结构

go-v3 为仓库根（即本目录）。v1(C++) / v2(Go) 前代版本封存于 `docs/legacy/` 只读快照，
不参与构建与 gofmt。CI 链：lint → test → eval-gate → docker（`.github/workflows/ci.yml`）。
