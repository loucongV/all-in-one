VERSION ?= $(shell cat VERSION 2>/dev/null || echo v0.1.0)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_TIME ?= $(shell date -Iseconds)

LDFLAGS := -s -w \
	-X github.com/loucongV/all-in-one/internal/version.Version=$(VERSION) \
	-X github.com/loucongV/all-in-one/internal/version.Commit=$(COMMIT) \
	-X github.com/loucongV/all-in-one/internal/version.BuildTime=$(BUILD_TIME)

TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build dist test vet clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ./aio ./cmd/aio

dist:
	rm -rf dist
	mkdir -p dist
	@for target in $(TARGETS); do \
		os=$${target%/*}; \
		arch=$${target#*/}; \
		out="dist/aio-$(VERSION)-$${os}-$${arch}"; \
		echo "Building $${out}..."; \
		CGO_ENABLED=0 GOOS=$${os} GOARCH=$${arch} go build -trimpath -ldflags "$(LDFLAGS)" -o "$${out}" ./cmd/aio; \
	done
	cd dist && sha256sum aio-$(VERSION)-* > SHA256SUMS && sha256sum -c SHA256SUMS

test:
	go test -v -race -count=1 ./...

vet:
	go vet ./...

clean:
	rm -rf dist ./aio
