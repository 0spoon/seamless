# Seamless Makefile. Go 1.25+, no CGO.
BINARY  := seamlessd
CLI     := seam
BIN_DIR := bin
PKG     := ./...
GO      ?= go

.PHONY: help build test test-race lint vet fmt tidy run doctor clean

help:
	@echo "Seamless targets:"
	@echo "  build      build ./bin/$(BINARY) and ./bin/$(CLI)"
	@echo "  test       run unit tests"
	@echo "  test-race  run unit tests with the race detector"
	@echo "  lint       run golangci-lint"
	@echo "  vet        run go vet"
	@echo "  fmt        gofmt the tree"
	@echo "  tidy       go mod tidy"
	@echo "  run        build and start the server (127.0.0.1:8081)"
	@echo "  doctor     build and run config + DB self-checks"
	@echo "  clean      remove build artifacts"

build:
	$(GO) build -o $(BIN_DIR)/$(BINARY) ./cmd/seamlessd
	$(GO) build -o $(BIN_DIR)/$(CLI) ./cmd/seam

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

lint:
	golangci-lint run

vet:
	$(GO) vet $(PKG)

fmt:
	gofmt -w .

tidy:
	$(GO) mod tidy

run: build
	$(BIN_DIR)/$(BINARY) serve

doctor: build
	$(BIN_DIR)/$(BINARY) doctor

clean:
	rm -rf $(BIN_DIR) coverage.*
