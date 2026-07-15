# Seamless Makefile. Go 1.25+, no CGO.
BINARY  := seamlessd
CLI     := seam
BIN_DIR := bin
PKG     := ./...
GO      ?= go

# Build metadata linked into the daemon (surfaced in /healthz, the MCP handshake,
# and the startup log so a stale running daemon is visible). A plain `go build`
# leaves these "unknown"; only `make build` stamps them.
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDDATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -X main.commit=$(COMMIT) -X main.buildDate=$(BUILDDATE)

# launchd service (macOS user LaunchAgent)
UID           := $(shell id -u)
SVC_LABEL     := org.thereisnospoon.seamless
SVC_TEMPLATE  := deploy/launchd/$(SVC_LABEL).plist
SVC_PLIST     := $(HOME)/Library/LaunchAgents/$(SVC_LABEL).plist
SVC_LOG       := $(HOME)/.seamless/seamlessd.log

# Release/prod install locations (stable, independent of this working tree).
# Override the prefix with `make install-prod PREFIX=/custom`.
PREFIX        ?= $(HOME)/.local
PREFIX_BIN    := $(PREFIX)/bin
PROD_CONFIG   := $(HOME)/.config/seamless/seamless.yaml

# gofmt over TRACKED files only. The go tool's ./... pattern skips dot-dirs, so
# build/vet/test/lint never see .claude/worktrees/ (other agents' checkouts of
# this same repo). gofmt takes paths, not packages, and walks the filesystem
# raw: `gofmt -l .` reports their drift as ours, and `gofmt -w .` rewrites
# their files underneath them mid-edit. Always go through this list.
GOFILES := git ls-files '*.go'

# Documentation site (cmd/docsgen). DOCS_OUT is committed; `make check` fails if
# it drifts from DOCS_SRC.
DOCS_SRC  := docs-src
DOCS_OUT  := docs/docs
DOCS_ADDR ?= 127.0.0.1:8899

.PHONY: help build test test-race bench lint vet fmt fmt-check check tidy run doctor console console-chrome dev \
	docs docs-check docs-serve \
	install-service install-hooks install-prod uninstall-prod _reload-service \
	uninstall-service start-service stop-service restart-service \
	service-status logs install-onboard-skill uninstall-onboard-skill clean

help:
	@echo "Seamless targets:"
	@echo "  build      build ./bin/$(BINARY) and ./bin/$(CLI)"
	@echo "  test       run unit tests"
	@echo "  test-race  run unit tests with the race detector"
	@echo "  bench      run hot-path benchmarks (BENCHTIME=1x for a quick smoke run)"
	@echo "  check      the full gate: build + vet + fmt-check + lint + test-race"
	@echo "  lint       run golangci-lint"
	@echo "  vet        run go vet"
	@echo "  fmt        gofmt tracked files"
	@echo "  fmt-check  fail if tracked files have gofmt drift"
	@echo "  docs       regenerate the docs site (docs-src/ -> docs/docs/, committed)"
	@echo "  docs-check fail if the committed docs site is stale (part of check)"
	@echo "  docs-serve regenerate and serve the site at $(DOCS_ADDR)"
	@echo "  tidy       go mod tidy"
	@echo "  run        build and start the server (127.0.0.1:8081)"
	@echo "  doctor     build and run config + DB self-checks"
	@echo "  console    open the console in a browser, pre-authenticated"
	@echo "  console-chrome  same, but force Google Chrome (for agent testing)"
	@echo "  Dev deploy (runs from ./bin -- fast iteration, tracks the working tree):"
	@echo "    dev                rebuild + restart the running service (quick iterate loop)"
	@echo "    install-service    render+install+load the launchd service, then restart it"
	@echo "    install-hooks      install Claude Code hooks pointing at ./bin + ./seamless.yaml"
	@echo "    uninstall-service  unload+remove the service"
	@echo "    start-service      load the installed service"
	@echo "    stop-service       unload the service (KeepAlive will not resurrect it)"
	@echo "    restart-service    restart the running service in place"
	@echo "    service-status     print the service's launchd state"
	@echo "    logs               follow the service log ($(SVC_LOG))"
	@echo "  Release/prod (snapshot to stable paths, independent of this repo):"
	@echo "    install-prod       copy bin+config to stable paths; point service+hook there"
	@echo "    uninstall-prod     remove prod service + copied binaries (config left in place)"
	@echo "  install-onboard-skill    install the /seam-onboard Claude Code skill"
	@echo "  uninstall-onboard-skill  remove the /seam-onboard skill"
	@echo "  clean      remove build artifacts"

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/seamlessd
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(CLI) ./cmd/seam

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

# Hot-path baseline benchmarks (recall, briefing, prompt matcher, event
# append + SSE fan-out). -run '^$' skips unit tests so only benchmarks run.
# Override BENCHTIME (e.g. BENCHTIME=1x) for a quick compile/smoke check.
BENCHTIME ?= 1s
bench:
	$(GO) test -run '^$$' -bench . -benchmem -benchtime $(BENCHTIME) $(PKG)

lint:
	golangci-lint run

vet:
	$(GO) vet $(PKG)

fmt:
	@$(GOFILES) | xargs gofmt -w

# Report drift instead of fixing it, so `make check` can fail on it.
fmt-check:
	@drift=$$($(GOFILES) | xargs gofmt -l); \
	    test -z "$$drift" || { echo "gofmt drift (run 'make fmt'):"; echo "$$drift"; exit 1; }

# The documentation site: markdown in docs-src/ -> static HTML in docs/docs/,
# committed and served by the same GitHub Pages config as the landing page.
# Two pages are generated from the code (the MCP tool reference reads
# mcp.Catalog(), the config reference reflects config.Defaults()), so a tool or
# key that changes makes the committed output stale -- which docs-check catches.
docs:
	$(GO) run ./cmd/docsgen -src $(DOCS_SRC) -out $(DOCS_OUT)

# Staleness gate. Regenerates into a temp dir and diffs, rather than rewriting
# the working tree and running `git diff`: this never mutates what you are
# editing, and -r catches untracked files that git diff cannot see (a page
# deleted from docs-src/ still committed under docs/docs/).
docs-check:
	@tmp=$$(mktemp -d) || exit 1; \
	    trap 'rm -rf "$$tmp"' EXIT; \
	    $(GO) run ./cmd/docsgen -src $(DOCS_SRC) -out "$$tmp/docs" >/dev/null || exit 1; \
	    diff -r "$$tmp/docs" $(DOCS_OUT) > "$$tmp/diff" 2>&1 \
	        || { echo "docs drift: $(DOCS_OUT) does not match $(DOCS_SRC) (run 'make docs' and commit)"; \
	             head -40 "$$tmp/diff"; exit 1; }

docs-serve: docs
	$(GO) run ./cmd/docsgen -src $(DOCS_SRC) -out $(DOCS_OUT) -serve $(DOCS_ADDR)

# The full gate: everything that must be green before declaring work done
# (AGENTS.md > "Verification before declaring done"). Sequential $(MAKE) calls
# rather than prerequisites so it fails at the first red step and stays ordered
# under `make -j`. Cheapest and most-likely-to-fail steps come first; test-race
# is last because it is the slowest by far.
check:
	@$(MAKE) build
	@$(MAKE) vet
	@$(MAKE) fmt-check
	@$(MAKE) docs-check
	@$(MAKE) lint
	@$(MAKE) test-race
	@echo "check: all green"

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

# Same, but force Google Chrome regardless of the default browser -- agents
# drive Chrome (Claude-in-Chrome), so this hands the auth cookie to the browser
# they can actually see.
console-chrome: build
	$(BIN_DIR)/$(BINARY) console-open --browser "Google Chrome"

# Quick dev loop: rebuild and get the latest code running. If the dev service is
# already installed (its plist points at ./bin), restart it in place -- fast, no
# plist re-render or bootout/bootstrap. Otherwise fall back to a full dev install
# (also covers switching back from a prod install). The hooks exec ./bin/seam
# fresh each session, so a rebuilt CLI is picked up without reinstalling hooks.
dev: build
	@if grep -qF '$(CURDIR)/$(BIN_DIR)/$(BINARY)' $(SVC_PLIST) 2>/dev/null; then \
	    $(MAKE) restart-service; \
	else \
	    echo "dev service not installed here; running install-service"; \
	    $(MAKE) install-service; \
	fi

# Boot the (freshly rendered) $(SVC_PLIST): evict any old instance, bootstrap
# with retry (bootout is async, so the label lingers briefly and bootstrapping
# too soon fails with "Bootstrap failed: 5: Input/output error"), kick it, and
# verify. Shared by install-service (dev) and install-prod.
_reload-service:
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
	    launchctl bootstrap gui/$(UID) $(SVC_PLIST) 2>/dev/null && break; \
	    sleep 1; \
	done
	@launchctl kickstart -k gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@launchctl print gui/$(UID)/$(SVC_LABEL) >/dev/null 2>&1 \
	    || { echo "ERROR: $(SVC_LABEL) failed to bootstrap; check $(SVC_LOG)"; exit 1; }

# DEV service: render the plist to run straight from this working tree (./bin +
# ./seamless.yaml) and (re)load it. Fast to iterate -- but `make build`, a branch
# switch, or a moved repo changes what the running service executes. Pair with
# `make install-hooks` for the matching SessionStart hook. Restarts the daemon.
install-service: build
	@mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.seamless
	@sed -e 's#__BINARY__#$(CURDIR)/$(BIN_DIR)/$(BINARY)#g' \
	     -e 's#__CONFIG__#$(CURDIR)/seamless.yaml#g' \
	     -e 's#__LOG__#$(SVC_LOG)#g' \
	     $(SVC_TEMPLATE) > $(SVC_PLIST)
	@$(MAKE) _reload-service
	@echo "installed dev launchd service $(SVC_LABEL) -> $(CURDIR)/$(BIN_DIR)/$(BINARY)"

# DEV hooks: install the Claude Code SessionStart/UserPromptSubmit/SessionEnd
# hooks pointing at this working tree's seam + seamless.yaml. install-prod
# installs the prod-path variant itself.
install-hooks: build
	@$(BIN_DIR)/$(BINARY) install-hooks

# RELEASE/PROD: snapshot the binaries + config to stable locations independent of
# this working tree, then point launchd AND the SessionStart hook at the copies.
# Survives `make build`, branch switches, and a moved/cleaned repo. Shares the
# data dir + port with dev (one instance per machine), so installing prod
# replaces the dev service. Override the location with `make install-prod PREFIX=/opt`.
install-prod: build
	@test -f seamless.yaml || { echo "ERROR: ./seamless.yaml not found (copy seamless.yaml.example)"; exit 1; }
	@mkdir -p $(PREFIX_BIN) $(dir $(PROD_CONFIG)) $(HOME)/Library/LaunchAgents $(HOME)/.seamless
	@install -m 0755 $(BIN_DIR)/$(BINARY) $(PREFIX_BIN)/$(BINARY)
	@install -m 0755 $(BIN_DIR)/$(CLI) $(PREFIX_BIN)/$(CLI)
	@if [ -f $(PROD_CONFIG) ]; then \
	    echo "config kept at $(PROD_CONFIG) (delete it to re-seed from ./seamless.yaml)"; \
	else \
	    install -m 0600 seamless.yaml $(PROD_CONFIG) && echo "seeded $(PROD_CONFIG) from ./seamless.yaml"; \
	fi
	@sed -e 's#__BINARY__#$(PREFIX_BIN)/$(BINARY)#g' \
	     -e 's#__CONFIG__#$(PROD_CONFIG)#g' \
	     -e 's#__LOG__#$(SVC_LOG)#g' \
	     $(SVC_TEMPLATE) > $(SVC_PLIST)
	@$(MAKE) _reload-service
	@SEAMLESS_CONFIG=$(PROD_CONFIG) $(PREFIX_BIN)/$(BINARY) install-hooks --seam $(PREFIX_BIN)/$(CLI)
	@echo "installed prod service + hooks (bin $(PREFIX_BIN), config $(PROD_CONFIG))"

uninstall-service:
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@rm -f $(SVC_PLIST)
	@echo "removed launchd service $(SVC_LABEL)"

# Remove the prod install: stop+remove the service and delete the copied
# binaries. The config at $(PROD_CONFIG) is left in place (it may hold local
# edits and a bearer key) -- delete it by hand to fully reset. Afterwards run
# `make install-service && make install-hooks` to return to the dev layout.
uninstall-prod: uninstall-service
	@rm -f $(PREFIX_BIN)/$(BINARY) $(PREFIX_BIN)/$(CLI)
	@echo "removed prod binaries from $(PREFIX_BIN); left $(PROD_CONFIG) in place"

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
