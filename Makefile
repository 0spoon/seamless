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

.PHONY: help build test test-race lint vet fmt tidy run doctor console \
	install-service uninstall-service start-service stop-service restart-service \
	service-status logs install-onboard-skill uninstall-onboard-skill clean

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
	@echo "  console    open the console in a browser, pre-authenticated"
	@echo "  Service (launchd):"
	@echo "    install-service    render+install+load the service, then restart it"
	@echo "    uninstall-service  unload+remove the service"
	@echo "    start-service      load the installed service"
	@echo "    stop-service       unload the service (KeepAlive will not resurrect it)"
	@echo "    restart-service    restart the running service in place"
	@echo "    service-status     print the service's launchd state"
	@echo "    logs               follow the service log ($(SVC_LOG))"
	@echo "  install-onboard-skill    install the /seam-onboard Claude Code skill"
	@echo "  uninstall-onboard-skill  remove the /seam-onboard skill"
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

# Open the console pre-authenticated: build, then have the daemon render a
# one-shot self-submitting login page (POSTs the static key, sets the cookie)
# and open it in the default browser. Requires the server to be running.
console: build
	$(BIN_DIR)/$(BINARY) console-open

# Render the plist template with absolute paths, install it, and (re)load it.
# Idempotent: an already-loaded service is booted out first. Restarts the daemon.
install-service: build
	@mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.seamless
	@sed -e 's#__BINARY__#$(CURDIR)/$(BIN_DIR)/$(BINARY)#g' \
	     -e 's#__CONFIG__#$(CURDIR)/seamless.yaml#g' \
	     -e 's#__LOG__#$(SVC_LOG)#g' \
	     $(SVC_TEMPLATE) > $(SVC_PLIST)
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@# bootout is async: the label lingers briefly while the old instance exits.
	@# Bootstrapping too soon fails with "Bootstrap failed: 5: Input/output error",
	@# so retry until the label is released.
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
	    launchctl bootstrap gui/$(UID) $(SVC_PLIST) 2>/dev/null && break; \
	    sleep 1; \
	done
	@launchctl kickstart -k gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@launchctl print gui/$(UID)/$(SVC_LABEL) >/dev/null 2>&1 \
	    && echo "installed launchd service $(SVC_LABEL) -> $(SVC_PLIST)" \
	    || { echo "ERROR: $(SVC_LABEL) failed to bootstrap; check $(SVC_LOG)"; exit 1; }

uninstall-service:
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@rm -f $(SVC_PLIST)
	@echo "removed launchd service $(SVC_LABEL)"

# Load the already-installed plist. Run install-service first if it is missing.
start-service:
	@test -f $(SVC_PLIST) || { echo "ERROR: $(SVC_PLIST) not found; run 'make install-service' first"; exit 1; }
	@launchctl bootstrap gui/$(UID) $(SVC_PLIST) 2>/dev/null \
	    && echo "started $(SVC_LABEL)" \
	    || echo "$(SVC_LABEL) already loaded (use 'make restart-service')"

# Unload the service. Bootout stops it for good; KeepAlive only resurrects a
# still-loaded job, so an unloaded one stays down until start/install.
stop-service:
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null \
	    && echo "stopped $(SVC_LABEL)" \
	    || echo "$(SVC_LABEL) was not loaded"

# Restart the running instance in place (fast; no plist re-render). Falls back
# to bootstrap when the service is installed but not currently loaded.
restart-service:
	@launchctl kickstart -k gui/$(UID)/$(SVC_LABEL) 2>/dev/null \
	    && echo "restarted $(SVC_LABEL)" \
	    || $(MAKE) start-service

service-status:
	@launchctl print gui/$(UID)/$(SVC_LABEL) 2>/dev/null \
	    | grep -E 'state|pid|program =|last exit' \
	    || echo "$(SVC_LABEL) is not loaded (run 'make start-service')"

logs:
	@test -f $(SVC_LOG) || { echo "no log yet at $(SVC_LOG)"; exit 1; }
	@tail -f $(SVC_LOG)

install-onboard-skill:
	@scripts/install-onboard-skill.sh

uninstall-onboard-skill:
	@scripts/uninstall-onboard-skill.sh

clean:
	rm -rf $(BIN_DIR) coverage.*
