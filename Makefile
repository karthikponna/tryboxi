SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOARCH_HOST := $(shell go env GOARCH)
LDFLAGS := -s -w -X github.com/karthikponna/tryboxi/internal/version.Version=$(VERSION)
AGENT_LDFLAGS := -s -w -X github.com/karthikponna/tryboxi/guest/server.Version=$(VERSION)
OUT := _out
BIN := $(OUT)/bin

.PHONY: all build build-darwin build-linux agent sign proto sqlc lint test test-linux e2e-darwin images kernel rootfs clean install-dev

all: build

## Guest agent: static Linux binary for both arches.
agent:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags '$(AGENT_LDFLAGS)' -o $(BIN)/sandbox-agent-linux-arm64 ./cmd/sandbox-agent
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(AGENT_LDFLAGS)' -o $(BIN)/sandbox-agent-linux-amd64 ./cmd/sandbox-agent

## Host binaries for this Mac (cgo for Virtualization.framework), then sign.
build-darwin: agent
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/sandboxd ./cmd/sandboxd
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/sandboxctl ./cmd/sandboxctl
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/sandbox-mcp ./cmd/sandbox-mcp
	$(MAKE) sign

## Static Linux host binaries (Firecracker backend).
build-linux: agent
	for a in arm64 amd64; do \
	  for c in sandboxd sandboxctl sandbox-mcp; do \
	    GOOS=linux GOARCH=$$a CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/linux-$$a/$$c ./cmd/$$c || exit 1; \
	  done; \
	done

build:
ifeq ($(shell uname -s),Darwin)
	$(MAKE) build-darwin
else
	$(MAKE) agent
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/sandboxd ./cmd/sandboxd
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/sandboxctl ./cmd/sandboxctl
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/sandbox-mcp ./cmd/sandbox-mcp
endif

## Ad-hoc sign sandboxd with the virtualization entitlement (required by macOS).
sign:
	codesign --force --entitlements build/vz.entitlements -s - $(BIN)/sandboxd

proto:
	BUF_CACHE_DIR=$(OUT)/cache/buf buf generate

sqlc:
	sqlc generate

lint:
	go vet ./...
	GOOS=linux GOARCH=arm64 go vet ./...
	gofmt -l . | grep -v '^_out/' | (! grep .)

test:
	go test ./...

## Linux-only guest/host unit tests, run inside a Linux container.
test-linux:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o $(OUT)/bin/proc.test ./guest/proc
	docker run --rm -v $(PWD)/$(OUT)/bin:/x:ro ubuntu:24.04 /x/proc.test -test.v

## Real microVM tests on this Mac (needs `make images`).
e2e-darwin: build-darwin
	SANDBOXD_E2E=1 go test -count=1 -v -timeout 15m ./test/e2e/...

## Guest kernel (Kata prebuilt, sha256 pinned) + base rootfs (built in Docker).
images: kernel rootfs

kernel:
	./scripts/images/fetch-kernel.sh

rootfs:
	./scripts/images/build-rootfs.sh

## Copy kernel + rootfs into the daemon data root and register the base template.
install-dev: build-darwin
	$(BIN)/sandboxd install --artifacts $(OUT)

clean:
	rm -rf $(BIN)
