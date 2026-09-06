# The admin console must be built manually before this Dockerfile is used:
# cd web/admin && npm ci && npm run build
# Its output in internal/console/dist is embedded into the Go binary below.
FROM golang:1.26-bookworm AS build
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=$GOPROXY go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY model/ ./model/
RUN CGO_ENABLED=0 go build -tags agentsandbox -o /out/agent-platform ./cmd/agent-platform

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agent-platform /usr/local/bin/agent-platform
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/agent-platform"]
CMD ["server"]
