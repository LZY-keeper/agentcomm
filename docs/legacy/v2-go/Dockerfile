# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download || true   # first build populates go.sum; tolerate empty
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/gateway ./cmd/gateway \
 && CGO_ENABLED=0 go build -trimpath -o /out/registry ./cmd/registry \
 && CGO_ENABLED=0 go build -trimpath -o /out/agent-math ./cmd/agent-math

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/gateway /out/registry /out/agent-math ./bin/
EXPOSE 8080 8500 5001 50051
ENTRYPOINT ["/app/bin/gateway"]
