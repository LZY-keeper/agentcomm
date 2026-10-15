# WASM 沙箱工具插件

第三方/不可信工具以 `.wasm` 形式在 wazero 沙箱里执行（支柱三）：跨平台、无 cgo、
**默认无文件/网络权限**，显式授权（`Permissions`）后才放开。

## 工具 ABI（语言无关）

stdin 读 JSON，stdout 写 JSON：

| 请求 | 响应 |
|------|------|
| `{"manifest": true}` | 工具自描述（name/description/input_schema/input_types/output_types） |
| `{"text": "..."}`（按 schema） | `{"text": "...", "is_error": false}` |

## 构建样例（需 TinyGo）

```bash
tinygo build -o wordcount.wasm -target wasi ./wordcount
```

把生成的 `.wasm` 放到 `WASM_DIR`（默认 `./plugins/wasm/bin`）。网关启动时加载该目录下
所有 `.wasm` 为沙箱工具，并经 manifest 约定接入联邦路由与 MCP server。

> 没装 TinyGo？没关系——原生工具（calculator/weather/read_file/code_review）开箱即用，
> WASM 是不可信工具的安全执行路径，按需启用。
