# Seamless Makefile. Go 1.25+, no CGO.
BINARY  := seamlessd
CLI     := seam
BIN_DIR := bin
PKG     := ./...
GO      ?= go

# launchd service (macOS user LaunchAgent)
UID           := $(shell id -u)
SVC_LABEL     := org.thereisnospoon.seamless
SVC_TEMPLATE  := deploy/launchd/$(SVC_LABEL).plist
SVC_PLIST     := $(HOME)/Library/LaunchAgents/$(SVC_LABEL).plist
SVC_LOG       := $(HOME)/.seamless/seamlessd.log

.PHONY: help build test test-race lint vet fmt tidy run doctor install-service uninstall-service clean

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
	@echo "  install-service    install+load the launchd service (127.0.0.1:8081)"
	@echo "  uninstall-service  unload+remove the launchd service"
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

# Render the plist template with absolute paths, install it, and (re)load it.
# Idempotent: an already-loaded service is booted out first. Restarts the daemon.
install-service: build
	@mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.seamless
	@sed -e 's#__BINARY__#$(CURDIR)/$(BIN_DIR)/$(BINARY)#g' \
	     -e 's#__CONFIG__#$(CURDIR)/seamless.yaml#g' \
	     -e 's#__LOG__#$(SVC_LOG)#g' \
	     $(SVC_TEMPLATE) > $(SVC_PLIST)
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@launchctl bootstrap gui/$(UID) $(SVC_PLIST)
	@launchctl kickstart -k gui/$(UID)/$(SVC_LABEL)
	@echo "installed launchd service $(SVC_LABEL) -> $(SVC_PLIST)"

uninstall-service:
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@rm -f $(SVC_PLIST)
	@echo "removed launchd service $(SVC_LABEL)"

clean:
	rm -rf $(BIN_DIR) coverage.*
