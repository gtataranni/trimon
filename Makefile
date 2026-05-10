VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS  := -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"

IMAGE    ?= trimon
TAG      ?= $(VERSION)

.PHONY: build test lint docker clean

## build: compile the trimon binary into ./bin/
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/trimon ./cmd/trimon

## test: run all unit tests
test:
	go test -race -count=1 ./...

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## docker: build the container image
docker:
	docker build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  -t $(IMAGE):$(TAG) .

## clean: remove build artifacts
clean:
	rm -rf bin/
