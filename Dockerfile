# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X main.Version=$(cat VERSION 2>/dev/null || echo 3.0.0)" -o /out/gateway ./cmd/gateway  && CGO_ENABLED=0 go build -trimpath -o /out/registry ./cmd/registry  && CGO_ENABLED=0 go build -trimpath -o /out/agent-math ./cmd/agent-math  && CGO_ENABLED=0 go build -trimpath -o /out/mcpserver ./cmd/mcpserver

FROM alpine:3.24
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/gateway /out/registry /out/agent-math /out/mcpserver ./bin/
EXPOSE 8080 8500 5001
ENTRYPOINT ["/app/bin/gateway"]
