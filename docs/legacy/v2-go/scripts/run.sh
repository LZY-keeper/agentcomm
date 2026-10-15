#!/usr/bin/env bash
# 启动 agentcomm 离线 demo（默认 mock LLM/embedding，免 API key）。
# 可选环境变量：
#   LLM_PROVIDER=qwen|openai|ollama  + 对应 API key → 真 LLM
#   EMBEDDING_PROVIDER=dashscope|openai + key → 真 embedding
#   READ_ROOT=/path  → read_file 工具沙箱根
#   WASM_DIR=./plugins/wasm/bin  → 加载 WASM 沙箱工具
#   DISTRIBUTED=1  → 额外启动 registry + 独立 math agent（演示分布式 A2A）
set -e
DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"
[ -f bin/gateway ] || make build

if [ "${DISTRIBUTED:-0}" = "1" ]; then
  mkdir -p logs
  echo "[distributed] starting registry + math-agent + gateway ..."
  ./bin/registry > logs/registry.log 2>&1 &
  echo $! > logs/registry.pid; sleep 1
  REGISTRY_URL=http://localhost:8500 ./bin/agent-math > logs/math.log 2>&1 &
  echo $! > logs/math.pid; sleep 1
  trap './scripts/stop.sh' INT TERM EXIT
fi

echo "=============================================="
echo " agentcomm · 智能 Agent 网关"
echo " Playground : http://localhost:8080"
echo " Metrics    : http://localhost:8080/metrics"
echo " Agent Card : http://localhost:8080/.well-known/agent-card.json"
echo " (Ctrl+C 退出)"
echo "=============================================="
./bin/gateway
