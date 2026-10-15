## agentcomm — 企业级智能 Agent 网关 / MCP 工具路由引擎
## 通用构建脚本。首次构建前先 `make tidy`（需联网拉依赖）。

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.Version=$(VERSION)
GOFLAGS  := -trimpath
GO       := go

.PHONY: all tidy proto build run test vet fmt bench-router docker up down clean help

all: build

## 拉取/规整依赖（构建机首次执行）
tidy:
	$(GO) mod tidy

## 由 proto 生成 gRPC/Go 桩代码（首次及 proto 变更后执行；需 protoc/protoc-gen-go/protoc-gen-go-grpc）
proto:
	@command -v protoc >/dev/null 2>&1 || { echo "请先安装 protoc 与 protoc-gen-go / protoc-gen-go-grpc（见 docs/BUILD.md）"; exit 1; }
	protoc --proto_path=proto \
	  --go_out=. --go_opt=module=agentcomm \
	  --go-grpc_out=. --go-grpc_opt=module=agentcomm \
	  proto/common.proto proto/ai_query.proto proto/agent_service.proto

## 编译离线可运行集到 bin/（gateway/agent-math/mcpserver/registry，无需 protoc）
build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/gateway    ./cmd/gateway
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/agent-math ./cmd/agent-math
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/mcpserver  ./cmd/mcpserver
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/registry   ./cmd/registry

## 编译 gRPC 客户端 + 启用 gRPC 的网关（需先 make proto 生成桩代码）
grpc: proto
	$(GO) build $(GOFLAGS) -tags grpc -ldflags "$(LDFLAGS)" -o bin/gateway-grpc ./cmd/gateway
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/cli ./cmd/cli

## 一键离线 demo（默认 mock provider，免 API key）：起全套并用浏览器打开 Playground
run: build
	./scripts/run.sh

## 测试
test:
	$(GO) test ./...

## 静态检查
vet:
	$(GO) vet ./...

fmt:
	gofmt -s -w .

## 路由引擎离线评测：输出 token 节省%、召回/精度（支柱一量化指标）
bench-router:
	$(GO) run ./cmd/bench-router

## Docker（需能拉镜像/联网）
docker:
	docker build -t agentcomm:$(VERSION) .

up:
	docker compose -f deploy/docker-compose.yml up -d --build

down:
	docker compose -f deploy/docker-compose.yml down

clean:
	rm -rf bin/

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
