.DEFAULT_GOAL := help

.PHONY: build build-k8s tidy compose-up compose-down compose-logs docker-sandbox docker-sandbox-python docker-sandbox-node docker-sandbox-k8s admin-build control-image-arm64 help

GO ?= go
BIN ?= bin/agent-platform

build:
	$(GO) build -o $(BIN) ./cmd/agent-platform

build-k8s:
	GOTOOLCHAIN=auto $(GO) build -tags agentsandbox -o $(BIN) ./cmd/agent-platform

tidy:
	$(GO) mod tidy

# Docker provides local PostgreSQL and MinIO only. Create the bucket with mc.
compose-up:
	docker compose up -d postgres minio

compose-down:
	docker compose down

compose-logs:
	docker compose logs -f postgres minio

docker-sandbox-k8s:
	docker build -f sandbox-runtime-go/Dockerfile -t agent-platform/sandbox-runtime:k8s .

docker-sandbox:
	docker build -f sandbox-runtime-go/Dockerfile -t agent-platform/sandbox-runtime:local .

docker-sandbox-python:
	docker build -f sandbox-runtime/Dockerfile.k8s -t agent-platform/sandbox-runtime:python ./sandbox-runtime

docker-sandbox-node:
	docker build -f sandbox-runtime-node/Dockerfile -t agent-platform/sandbox-runtime:node ./sandbox-runtime-node

# The console is intentionally built outside Docker so platform-image builds
# do not install Node dependencies. Its dist files are embedded by Go.
admin-build:
	cd web/admin && npm run build

# Builds and verifies locally; pushing remains an explicit docker push command.
# CONTROL_REPOSITORY defaults to an example registry; set it to your image repo.
control-image-arm64:
	@tag="$(CONTROL_TAG)"; \
	if [ -z "$$tag" ]; then tag="tke-arm64-$$(date +%Y%m%d%H%M%S)"; fi; \
	repository="$(CONTROL_REPOSITORY)"; \
	if [ -z "$$repository" ]; then repository="registry.example.com/agent-platform/control-plane"; fi; \
	image="$$repository:$$tag"; \
	DOCKER_BUILDKIT=0 docker build --platform linux/arm64 -f Dockerfile.arm64 -t "$$image" .; \
	docker image inspect "$$image" --format 'platform={{.Os}}/{{.Architecture}} user={{.Config.User}}'

help:
	@printf '%s\n' \
	  'Safe deployment shortcuts (no target runs by default):' \
	  '  make admin-build            Build the embedded admin console outside Docker.' \
	  '  make control-image-arm64    Build/verify ARM64 control image locally; no push.' \
	  '' \
  'Variables: CONTROL_TAG, RUNTIME_TAG, NAMESPACE, RELEASE.'
