VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS  := -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

IMAGE    ?= trimon
TAG      ?= $(VERSION)

# Container runtime to use for build targets. Override with: make container CONTAINER_RUNTIME=docker
CONTAINER_RUNTIME ?= podman

.PHONY: build test lint container docker podman clean release dev-stack dev-stack-down

## build: compile the trimon binary into ./bin/
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/trimon ./cmd/trimon

## test: run all unit tests
test:
	go test -race -count=1 ./...

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## container: build the container image using CONTAINER_RUNTIME (default: podman)
##            examples: make container
##                      make container CONTAINER_RUNTIME=docker
container:
	$(CONTAINER_RUNTIME) build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  -t $(IMAGE):$(TAG) .

## podman: build the container image with podman (alias for: make container CONTAINER_RUNTIME=podman)
podman: CONTAINER_RUNTIME=podman
podman: container

## docker: build the container image with docker (alias for: make container CONTAINER_RUNTIME=docker)
docker: CONTAINER_RUNTIME=docker
docker: container

## release: create an annotated git tag — usage: make release V=v0.1.0
release:
	@[ -n "$(V)" ] || { echo "Usage: make release V=v0.1.0"; exit 1; }
	@echo "$(V)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "V must match vMAJOR.MINOR.PATCH (e.g. v0.1.0)"; exit 1; }
	git tag -a $(V) -m "Release $(V)"
	@echo "Tag $(V) created. Push with: git push origin $(V)"

## clean: remove build artifacts
clean:
	rm -rf bin/

## dev-stack: start the local observability stack (Grafana, Prometheus, OTel Collector)
dev-stack:
	$(CONTAINER_RUNTIME) compose -f examples/local-stack/docker-compose.yml up -d
	@echo ""
	@echo "Dev stack is up:"
	@echo "  Grafana        http://localhost:3000"
	@echo "  Prometheus     http://localhost:9090"
	@echo "  OTLP gRPC      localhost:4317"
	@echo "  OTLP HTTP      http://localhost:4318"

## dev-stack-down: stop the local observability stack and remove volumes
dev-stack-down:
	$(CONTAINER_RUNTIME) compose -f examples/local-stack/docker-compose.yml down -v
