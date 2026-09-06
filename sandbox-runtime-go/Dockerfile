FROM golang:1.26-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux
COPY go.mod go.sum ./
COPY internal/contracts ./internal/contracts
COPY sandbox-runtime-go/go.mod ./sandbox-runtime-go/go.mod
COPY sandbox-runtime-go ./sandbox-runtime-go
WORKDIR /src/sandbox-runtime-go
RUN go build -trimpath -ldflags='-s -w' -o /out/runtime ./cmd/runtime

FROM python:3.12-alpine
RUN apk add --no-cache tini curl
WORKDIR /app
COPY --from=builder /out/runtime /app/bin/runtime
COPY sandbox-runtime-go/config/plugins.json /app/plugins.json
RUN mkdir -p /app/workspace /app/output /app/.runtime-trace /app/input \
    && addgroup -S runtime && adduser -S -G runtime -u 1000 runtime \
    && chown -R runtime:runtime /app
USER 1000:1000
EXPOSE 8888
ENTRYPOINT ["/sbin/tini", "--", "/app/bin/runtime", "--serve", "--port", "8888"]
