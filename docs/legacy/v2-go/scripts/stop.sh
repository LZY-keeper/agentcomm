#!/usr/bin/env bash
# 停止 agentcomm 进程（DISTRIBUTED 模式启动的 registry / math-agent）。
DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"
for f in logs/registry.pid logs/math.pid; do
  [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true
  rm -f "$f"
done
pkill -f "bin/gateway" 2>/dev/null || true
echo "stopped."
