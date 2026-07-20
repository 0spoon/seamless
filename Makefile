# Seamless Makefile. Go 1.25+, no CGO.
BINARY  := seamlessd
CLI     := seam
BIN_DIR := bin
PKG     := ./...
GO      ?= go

# Build metadata linked into the daemon (surfaced in /healthz, the MCP handshake,
# and the startup log so a stale running daemon is visible). A plain `go build`
# leaves these at their in-source defaults ("unknown"/"0.0.0-dev"); only
# `make build` stamps them.
#
# VERSION derives from the git tag exactly the way goreleaser does (nearest v*
# tag, leading v stripped) so `make install` and a released binary report the
# same number -- the tag is the single source of truth, never a hand-edited
# constant. Off a tag it falls back to the dev sentinel; the +COMMIT suffix
# buildVersion() appends still pins the exact build.
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDDATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION   := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)
VERSION   := $(if $(VERSION),$(VERSION),0.0.0-dev)
LDFLAGS   := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILDDATE)

# launchd service (macOS user LaunchAgent)
UID           := $(shell id -u)
SVC_LABEL     := org.thereisnospoon.seamless
SVC_TEMPLATE  := deploy/launchd/$(SVC_LABEL).plist
SVC_PLIST     := $(HOME)/Library/LaunchAgents/$(SVC_LABEL).plist
SVC_LOG       := $(HOME)/.seamless/seamlessd.log

# Install locations: stable paths, independent of this working tree. `make
# install` snapshots the binaries and config here and points launchd + the Claude
# Code hooks at the copies, so `make build`, a branch switch, or a moved repo
# never change what the live daemon and every agent's hooks execute.
# Override the prefix with `make install PREFIX=/custom`.
PREFIX        ?= $(HOME)/.local
PREFIX_BIN    := $(PREFIX)/bin
CONFIG_DIR    := $(HOME)/.config/seamless
CONFIG        := $(CONFIG_DIR)/seamless.yaml

# gofmt over TRACKED files only. The go tool's ./... pattern skips dot-dirs, so
# build/vet/test/lint never see .claude/worktrees/ (other agents' checkouts of
# this same repo). gofmt takes paths, not packages, and walks the filesystem
# raw: `gofmt -l .` reports their drift as ours, and `gofmt -w .` rewrites
# their files underneath them mid-edit. Always go through this list.
GOFILES := git ls-files '*.go'

# The bind address lives in the config (`addr:`) -- that is the single source of
# truth, because the daemon reads it there and nothing else can override it
# without becoming a second one. So this Makefile *reads* the port, never owns
# it: change `addr:` in the config, `make install`, and the health poll and help
# text follow. Deliberately not a make variable: baking SEAMLESS_ADDR into the
# plist would let env silently beat the config file, so editing `addr:` would
# appear to do nothing.
#
# Recursive (=), not :=, so it is read at use time -- $(CONFIG) may not exist
# until _seed-config runs during the first install. DEFAULT_ADDR mirrors
# config.Defaults() for the pre-install case.
# (No `#` in the character class: make strips from an unescaped `#` to end of
# line even inside $(shell ...), which silently truncates the call. Excluding
# whitespace already drops any trailing YAML comment, since YAML requires a
# space before `#`.)
DEFAULT_ADDR := 127.0.0.1:8081
CONFIG_ADDR   = $(shell sed -n 's/^addr:[[:space:]]*"\{0,1\}\([^"[:space:]]*\).*/\1/p' $(CONFIG) 2>/dev/null | head -1)
ADDR          = $(or $(CONFIG_ADDR),$(DEFAULT_ADDR))

# Documentation site (cmd/docsgen). DOCS_OUT is committed; `make check` fails if
# it drifts from DOCS_SRC.
DOCS_SRC  := docs-src
DOCS_OUT  := docs/docs
DOCS_ADDR ?= 127.0.0.1:8899

# The one-command installer (curl -fsSL https://thereisnospoon.org/install | sh).
# It lives at the site root because GitHub Pages serves docs/ verbatim, so this
# path IS the published URL -- there is no copy to keep in step. docsgen never
# touches it: it owns $(DOCS_OUT) only. PS_INSTALLER is the Windows companion,
# served the same way at /install.ps1.
INSTALLER    := docs/install
PS_INSTALLER := docs/install.ps1

.PHONY: help build test test-race bench lint vet vulncheck fmt fmt-check check check-fast tidy run doctor console console-chrome \
	docs docs-check docs-serve installer-check site-check site-stamp release-snapshot install-git-hooks uninstall-git-hooks \
	install uninstall update _seed-config _reload-service _wait-healthy start stop restart status \
	logs install-onboard-skill uninstall-onboard-skill \
	install-research-skill uninstall-research-skill clean

help:
	@echo "Seamless targets:"
	@echo "  build      compile ./bin/$(BINARY) and ./bin/$(CLI) (touches nothing live)"
	@echo "  test       run unit tests"
	@echo "  test-race  run unit tests with the race detector"
	@echo "  bench      run hot-path benchmarks (BENCHTIME=1x for a quick smoke run)"
	@echo "  check      the full gate: build + vet + fmt + docs + installer + site + lint + vulncheck + test-race"
	@echo "  check-fast the pre-commit subset: same minus build and test-race"
	@echo "  lint       run golangci-lint"
	@echo "  vet        run go vet"
	@echo "  vulncheck  run govulncheck against the vuln DB (part of check; needs network)"
	@echo "  fmt        gofmt tracked files"
	@echo "  fmt-check  fail if tracked files have gofmt drift"
	@echo "  docs       regenerate the docs site (docs-src/ -> docs/docs/, committed)"
	@echo "  docs-check fail if the committed docs site is stale (part of check)"
	@echo "  docs-serve regenerate and serve the site at $(DOCS_ADDR)"
	@echo "  installer-check   parse-check $(INSTALLER) (part of check)"
	@echo "  site-check        fail if the landing page drifts from the installer/CLI (part of check)"
	@echo "  site-stamp        restamp the landing page's static asset ?v= cache-busters"
	@echo "  release-snapshot  dry-run the release build into dist/ (needs goreleaser)"
	@echo "  tidy       go mod tidy"
	@echo "  run        build and start the server in the foreground ($(ADDR))"
	@echo "  doctor     build and run config + DB self-checks"
	@echo "  console    open the console in a browser, pre-authenticated"
	@echo "  console-chrome  same, but force Google Chrome (for agent testing)"
	@echo "  Deploy (snapshots to stable paths; this is also the iterate loop):"
	@echo "    install            build + copy bin/config to $(PREFIX_BIN) + $(CONFIG_DIR),"
	@echo "                       point the service + hooks there, and restart"
	@echo "    uninstall          remove service, hooks, MCP, skills + binaries (config/data kept;"
	@echo "                       PURGE=1 also deletes config + ~/.seamless)"
	@echo "    update             upgrade in place to the latest release (CHECK=1 only reports)"
	@echo "    start              start the installed service (launchd/systemd/task)"
	@echo "    stop               stop the installed service"
	@echo "    restart            restart the installed service in place"
	@echo "    status             print the installed service's state"
	@echo "    logs               follow the service log ($(SVC_LOG))"
	@echo "  install-git-hooks        enable .githooks/ (pre-commit runs check-fast)"
	@echo "  uninstall-git-hooks      disable .githooks/"
	@echo "  install-onboard-skill    install the /seam-onboard Claude Code skill"
	@echo "  uninstall-onboard-skill  remove the /seam-onboard skill"
	@echo "  install-research-skill   install the /seam-research Claude Code skill"
	@echo "  uninstall-research-skill remove the /seam-research skill"
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

# Known-vulnerability gate. Reachability-based (govulncheck reports only vulns
# whose vulnerable symbols this code actually calls), so it stays quiet about
# the long tail in transitive deps and fails only on something real.
#
# A hard requirement, like golangci-lint -- an advisory "skip if missing" gate
# is the kind that is green on your laptop and red nowhere, which is how the
# 16 hits in the 2026-07-19 audit accumulated unseen in the first place. It is
# in `check` but NOT `check-fast`: it queries vuln.go.dev, and the pre-commit
# hook has to work on a plane.
vulncheck:
	@command -v govulncheck >/dev/null \
	    || { echo "ERROR: govulncheck not found (go install golang.org/x/vuln/cmd/govulncheck@latest)"; exit 1; }
	govulncheck $(PKG)

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
	@$(MAKE) installer-check
	@$(MAKE) site-check
	@$(MAKE) lint
	@$(MAKE) vulncheck
	@$(MAKE) test-race
	@echo "check: all green"

# The fast half of `check`: everything except test-race, which is ~37s against
# ~3s for these six combined. This is what .githooks/pre-commit runs, and it is
# the quick iterate loop by hand. `build` is omitted as redundant -- vet
# type-checks, so it already fails on anything that would not compile.
# Cheapest-first, same as `check`. NOT a substitute for the full gate.
check-fast:
	@$(MAKE) fmt-check
	@$(MAKE) vet
	@$(MAKE) docs-check
	@$(MAKE) installer-check
	@$(MAKE) site-check
	@$(MAKE) lint
	@echo "check-fast: all green (test-race not run -- 'make check' is the full gate)"

# $(INSTALLER) is served verbatim at https://thereisnospoon.org/install and is
# the first command a new user ever runs, so a syntax error in it is a broken
# front door that no Go test can see. `sh -n` is the gate because it needs
# nothing installed. shellcheck is the better tool and runs when it happens to
# be present, but only advisory: a gate that fails based on what you have
# installed is a gate that is green in CI and red on your laptop.
installer-check:
	@sh -n $(INSTALLER) || { echo "ERROR: $(INSTALLER) has a syntax error"; exit 1; }
	@if command -v shellcheck >/dev/null; then shellcheck -s sh $(INSTALLER) || true; fi
	@echo "installer-check: $(INSTALLER) parses"
	@# The Windows installer is served the same way, so a syntax error in it is the
	@# same broken front door. pwsh is the parser (the .ps1 analog of `sh -n`);
	@# unlike shellcheck this fails on a real parse error, but only when pwsh is
	@# present (CI's ubuntu runner has it), so the gate is never tool-dependent.
	@if command -v pwsh >/dev/null 2>&1; then \
	    pwsh -NoProfile -Command '$$err=$$null; [void][System.Management.Automation.Language.Parser]::ParseFile("$(PS_INSTALLER)", [ref]$$null, [ref]$$err); if ($$err.Count) { $$err | ForEach-Object { [Console]::Error.WriteLine($$_.Message) }; exit 1 }' \
	        || { echo "ERROR: $(PS_INSTALLER) has a syntax error"; exit 1; }; \
	    echo "installer-check: $(PS_INSTALLER) parses"; \
	else \
	    echo "installer-check: pwsh not found; $(PS_INSTALLER) parse skipped (advisory)"; \
	fi

# docs/index.html is the one page with no generator behind it, so it is the one
# page that can lie indefinitely: the curl|sh installer shipped in d1e926b with
# every generated surface correct and this gate green, while the hero pill still
# said `go install`. The script checks only what is mechanically verifiable --
# the install command each surface teaches, the subcommands the page names, and
# whether the copy buttons copy what they show. The prose is still on you.
# Paths and the canonical install command live in the script, not here, so it
# stays runnable on its own and there is one place to change them.
site-check:
	@scripts/site-check.sh

# Restamp static/site.css and static/site.js with a content-hash ?v= in the
# hand-written landing page, so a changed asset gets a fresh URL that the CDN
# edge cache has never seen instead of serving stale behind its TTL. Run after
# editing either asset; site-check fails until the stamped hash matches.
site-stamp:
	@scripts/site-stamp.sh

# Git hooks. Committed under .githooks/ because .git/hooks is neither committed
# nor shared between worktrees. core.hooksPath is repo-local config, so enabling
# them is a per-clone opt-in rather than something a checkout imposes on you.
# The path stays relative: git resolves it against each worktree's top level, so
# linked worktrees under .claude/worktrees/ run their own checkout's copy.
# (Distinct from `install-hooks`, which installs the Claude Code hooks.)
install-git-hooks:
	@git config core.hooksPath .githooks
	@echo "git hooks enabled: pre-commit runs 'make check-fast' (bypass: git commit --no-verify)"

uninstall-git-hooks:
	@git config --unset core.hooksPath 2>/dev/null || true
	@echo "git hooks disabled"

# Local dry-run of the release pipeline (.goreleaser.yaml): validates the
# config and cross-compiles every platform into dist/ without tagging or
# publishing. Real releases run in CI on a v* tag (.github/workflows/release.yml).
# --skip=sign: the `signs:` stage is keyless cosign, which needs the GitHub OIDC
# token that only exists inside the release workflow. Off a runner it falls back
# to an interactive browser flow, so a local dry-run would hang instead of
# validating the build. Signing is exercised for real on a v* tag.
release-snapshot:
	@command -v goreleaser >/dev/null \
	    || { echo "ERROR: goreleaser not found (brew install goreleaser)"; exit 1; }
	goreleaser release --snapshot --clean --skip=sign

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

# Boot the (freshly rendered) $(SVC_PLIST): evict any old instance, bootstrap
# with retry (bootout is async, so the label lingers briefly and bootstrapping
# too soon fails with "Bootstrap failed: 5: Input/output error"), kick it, and
# verify. Only needed when the plist itself changed; `install` kickstarts in
# place otherwise.
_reload-service:
	@launchctl bootout gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
	    launchctl bootstrap gui/$(UID) $(SVC_PLIST) 2>/dev/null && break; \
	    sleep 1; \
	done
	@launchctl kickstart -k gui/$(UID)/$(SVC_LABEL) 2>/dev/null || true
	@launchctl print gui/$(UID)/$(SVC_LABEL) >/dev/null 2>&1 \
	    || { echo "ERROR: $(SVC_LABEL) failed to bootstrap; check $(SVC_LOG)"; exit 1; }

# The one deploy path: snapshot the binaries + config to stable locations
# independent of this working tree, then point launchd AND the Claude Code hooks
# at the copies. Because nothing live resolves through ./bin, `make build`, a
# branch switch, or a moved/cleaned repo cannot change what the running daemon
# and every agent's hooks execute -- swapping them is this target, deliberately.
#
# It is also the iterate loop: when the rendered plist is unchanged (the common
# case) this skips the bootout/bootstrap dance and kickstarts in place, so the
# marginal cost over `make build` is two file copies.
#
# One instance per machine (data dir ~/.seamless; bind from the config's addr).
# Override the location with `make install PREFIX=/opt`.
install: build
	@mkdir -p $(PREFIX_BIN) $(CONFIG_DIR) $(HOME)/Library/LaunchAgents $(HOME)/.seamless
	@install -m 0755 $(BIN_DIR)/$(BINARY) $(PREFIX_BIN)/$(BINARY)
	@install -m 0755 $(BIN_DIR)/$(CLI) $(PREFIX_BIN)/$(CLI)
	@$(MAKE) _seed-config
	@tmp=$$(mktemp) || exit 1; \
	    sed -e 's#__BINARY__#$(PREFIX_BIN)/$(BINARY)#g' \
	        -e 's#__CONFIG__#$(CONFIG)#g' \
	        -e 's#__LOG__#$(SVC_LOG)#g' \
	        $(SVC_TEMPLATE) > $$tmp || exit 1; \
	    if cmp -s $$tmp $(SVC_PLIST) && launchctl print gui/$(UID)/$(SVC_LABEL) >/dev/null 2>&1; then \
	        rm -f $$tmp; \
	        launchctl kickstart -k gui/$(UID)/$(SVC_LABEL) >/dev/null 2>&1 \
	            && echo "restarted $(SVC_LABEL) (plist unchanged)" \
	            || { echo "ERROR: kickstart failed; check $(SVC_LOG)"; exit 1; }; \
	    else \
	        install -m 0644 $$tmp $(SVC_PLIST) || exit 1; \
	        rm -f $$tmp; \
	        $(MAKE) _reload-service; \
	    fi
	@SEAMLESS_CONFIG=$(CONFIG) $(PREFIX_BIN)/$(BINARY) install-hooks --seam $(PREFIX_BIN)/$(CLI)
	@$(MAKE) _wait-healthy
	@$(PREFIX_BIN)/$(BINARY) install-summary --bin-dir $(PREFIX_BIN) --config $(CONFIG) --bins $(BINARY),$(CLI)

# launchd returns as soon as it has *started* the process, but the daemon binds
# its listener ~100ms later. Without this, `make install && make doctor` -- the
# documented upgrade sequence -- races a server that is not accepting yet and
# reports a failure that fixes itself. Poll until it actually answers, so a green
# install means it is serving.
_wait-healthy:
	@for i in $$(seq 1 50); do \
	    curl -sf --max-time 1 -o /dev/null "http://$(ADDR)/healthz" 2>/dev/null && exit 0; \
	    sleep 0.1; \
	done; \
	echo "ERROR: no /healthz from $(ADDR) after 5s; check $(SVC_LOG)"; exit 1

# Seed $(CONFIG) on first install only -- never clobber a config that may hold an
# edited bearer key. ./seamless.yaml (gitignored, the pre-install layout's live
# config) wins over the committed template so an existing setup keeps its keys;
# a fresh clone gets the example with a generated mcp.api_key, ready to run.
_seed-config:
	@if [ -f $(CONFIG) ]; then \
	    echo "config kept at $(CONFIG) (delete it to re-seed)"; \
	elif [ -f seamless.yaml ]; then \
	    install -m 0600 seamless.yaml $(CONFIG) && echo "seeded $(CONFIG) from ./seamless.yaml"; \
	else \
	    install -m 0600 seamless.yaml.example $(CONFIG); \
	    KEY=$$(openssl rand -hex 32); \
	    sed -i '' -e "/^mcp:/,/api_key:/ s/api_key: \"\"/api_key: \"$$KEY\"/" $(CONFIG); \
	    echo "seeded $(CONFIG) from seamless.yaml.example with a generated mcp.api_key"; \
	fi

# Full uninstall, delegated to the Go command so there is ONE cross-OS
# implementation (service + hooks + MCP + skills + binaries) instead of the old
# macOS-only launchctl+rm, which reversed neither the hooks/MCP/skills nor the
# Linux/Windows services. --yes because make is non-interactive; --install-dir
# honors `make install PREFIX=...`. $(CONFIG) and ~/.seamless are kept (your
# memories and notes are markdown that outlive the program) unless you opt in:
# `make uninstall PURGE=1`. `build` first so a clean tree still has a binary to
# run -- it tears down the INSTALLED service and $(PREFIX_BIN) binaries, a
# different file from ./bin, so it never deletes the one running it.
PURGE ?=
uninstall: build
	@$(BIN_DIR)/$(BINARY) uninstall --yes --install-dir $(PREFIX_BIN) $(if $(strip $(PURGE)),--purge,)

# Self-update to the latest published release, delegated to the Go binary so the
# release-fetch + checksum + swap logic keeps ONE home (the installer scripts the
# binary re-runs) -- the same delegation `uninstall` and the lifecycle verbs use.
# `build` first so a clean tree has a binary to run. Note this installs the latest
# RELEASE into $(PREFIX_BIN), which may be OLDER than this clone's HEAD -- to
# deploy your local build instead, use `make install`. CHECK=1 only reports
# installed vs latest and changes nothing.
CHECK ?=
update: build
	@$(BIN_DIR)/$(BINARY) update $(if $(strip $(CHECK)),--check,)

# Service lifecycle, delegated to the Go binary so there is ONE cross-OS
# implementation (launchd / systemd --user / Scheduled Task) instead of the old
# macOS-only launchctl targets -- the same move `uninstall` made. `build` first
# so a clean tree has a binary to run; it drives the INSTALLED service wherever
# the installer placed it. A not-installed service prints a hint, not a crash.
start: build
	@$(BIN_DIR)/$(BINARY) start

stop: build
	@$(BIN_DIR)/$(BINARY) stop

restart: build
	@$(BIN_DIR)/$(BINARY) restart

status: build
	@$(BIN_DIR)/$(BINARY) status

logs:
	@test -f $(SVC_LOG) || { echo "no log yet at $(SVC_LOG)"; exit 1; }
	@tail -f $(SVC_LOG)

install-onboard-skill:
	@scripts/install-skill.sh seam-onboard

uninstall-onboard-skill:
	@scripts/uninstall-skill.sh seam-onboard

install-research-skill:
	@scripts/install-skill.sh seam-research

uninstall-research-skill:
	@scripts/uninstall-skill.sh seam-research

clean:
	rm -rf $(BIN_DIR) dist coverage.*
