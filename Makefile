VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS  := -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

IMAGE    ?= trimon
TAG      ?= $(VERSION)

# Container runtime to use for build targets. Override with: make container CONTAINER_RUNTIME=podman
CONTAINER_RUNTIME ?= docker

.PHONY: build test lint gen-docs check-docs container docker podman clean release dev-stack demo dev-stack-down smoke

## build: compile the trimon binary into ./bin/
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/trimon ./cmd/trimon

## test: run all unit tests
test:
	go test -race -count=1 ./...

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## gen-docs: regenerate generated docs (the metrics reference table) from code
gen-docs:
	go run ./cmd/gen-metrics-docs

## check-docs: fail if `make gen-docs` would change any committed generated doc
check-docs: gen-docs
	@git diff --exit-code -- docs/metrics.md || { \
	  echo ""; \
	  echo "docs/metrics.md is stale — run 'make gen-docs' and commit the result."; \
	  exit 1; \
	}

## container: build the container image using CONTAINER_RUNTIME (default: docker)
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

## dev-stack: build trimon and start the lean runtime (trimon + OTel Collector).
##            This is the probe smoke/runtime harness. Add the demo viz layer
##            (Prometheus + Grafana) with `make demo`.
dev-stack:
	$(CONTAINER_RUNTIME) compose -f examples/local-stack/docker-compose.yml up --build -d
	@echo ""
	@echo "Dev stack (runtime) is up:"
	@echo "  trimon /metrics   http://localhost:8080/metrics"
	@echo "  trimon /healthz   http://localhost:8080/healthz"
	@echo "  otelcol /metrics  http://localhost:8889/metrics   (OTLP push, re-exposed)"
	@echo "  OTLP gRPC         localhost:4317"
	@echo "  OTLP HTTP         http://localhost:4318"
	@echo ""
	@echo "  probe stdout:     $(CONTAINER_RUNTIME) logs -f trimon-dev"
	@echo "  add Grafana:      make demo"

## demo: start the full observability stack (trimon, OTel Collector, Prometheus, Grafana)
demo:
	$(CONTAINER_RUNTIME) compose -f examples/local-stack/docker-compose.yml --profile demo up --build -d
	@echo ""
	@echo "Demo stack is up:"
	@echo "  trimon /metrics  http://localhost:8080/metrics"
	@echo "  trimon /healthz  http://localhost:8080/healthz"
	@echo "  Grafana          http://localhost:3000  (admin/admin)"
	@echo "  Prometheus       http://localhost:9090"
	@echo "  OTLP gRPC        localhost:4317"
	@echo "  OTLP HTTP        http://localhost:4318"

## dev-stack-down: stop the local stack (runtime and demo) and remove volumes
dev-stack-down:
	$(CONTAINER_RUNTIME) compose -f examples/local-stack/docker-compose.yml --profile demo down -v

## smoke: end-to-end smoke test — build+start the dev-stack, assert every probe
##        type reports through /metrics and reaches the collector, then tear down.
##        Needs outbound network (probes hit public targets). Pass flags through:
##        make smoke ARGS="--keep"
smoke:
	CONTAINER_RUNTIME=$(CONTAINER_RUNTIME) ./scripts/smoke.sh $(ARGS)
