# Changelog

> Every Seamless release, newest first - what shipped and when, regenerated from the git tags at each release.

Every release, newest first. The entries mirror the notes on
[GitHub Releases](https://github.com/0spoon/seamless/releases) - the same
commit subjects, grouped the same way, with housekeeping (docs, CI, dependency
bumps) filtered out. Each heading links the release's downloads and checksums.

Install with one command ([quickstart](https://thereisnospoon.org/docs/quickstart/)), or update an existing
install in place with `seamlessd update`.

## v0.4.6 {#v0-4-6}

Released 2026-07-29 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.4.6)

### Features

- feat(deploy): add the Glama directory image
- feat(bench): replace the scenario suite with five mechanism scenarios
- feat(bench): authenticate the agent inside an arm
- feat(bench): add the edge-caching briefing-catch scenario
- feat(bench): results, uplift report, and version comparison
- feat(bench): headless runner cmd/seambench run
- feat(bench): grade a captured run from its artifact directory
- feat(bench): freeze the run-artifact contract between runner and grader
- feat(branding): carve out scripts/branding with a verbatim distill tool
- feat(gardener): rebuild the console queue as an inbox with undo
- feat(gardener): settle implemented-but-unapproved captures as shipped, not abandoned
- feat(bench): benchmark scenario + conditions model in internal/bench
- feat(fixture): generalize the scene fixture into a dual-mode harness
- feat(docs): language labels on fenced code blocks
- feat(docs): card micro-affordances - hover arrow, section page counts
- feat(site,docs): polish sweep - clipboard fallback, anchors, print, hints
- feat(docs): inline TOC below the right-rail breakpoint, search snippets
- feat(projects): adopt a moved repo's project instead of minting slug-2
- feat(site): mobile navigation release - drawer scrim, landing menu
- feat(docs): global context chip, demoted per-page bar, hidden-block count
- feat(docs): route from the top of the docs home
- feat(docs): one reading context - OS/client picker, router home, single-home IA

### Fixes

- fix(mcp): resolve the five recurring agent tool errors the gardener collected
- fix(bench): bind arms through the physical repo path
- fix(console): preserve Gardener reader position
- fix(console): polish Gardener layout
- fix(install): reload the launchd job instead of kickstarting in place
- fix(mcp): attribute session tool.calls to the session they operate on
- fix(site): put the landing hamburger in the left corner
- fix(site): store the UA-refined OS from the landing switch
- fix(changelog): exempt the release commit from the drift check

### Other

- perf(docs): prefetch same-origin pages on hover intent
- perf(site,docs): preload the three variable woff2 fonts

## v0.4.5 {#v0-4-5}

Released 2026-07-26 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.4.5)

### Features

- feat(install): order install targets Claude-first
- feat(briefing): order plans and ready tasks by recency

### Fixes

- fix(mcp): preserve memory frontmatter on update, add memory_write tags
- fix(docs): keep tool names whole in the five-card flow figure

## v0.4.4 {#v0-4-4}

Released 2026-07-24 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.4.4)

### Features

- feat(briefing): grouped-section layout + recall kind-browse mode
- feat(memory): runtime self-teaching for the stage header contract
- feat(guidance): teach the stage header contract at write time
- feat(gardener): rekind proposal type -- propose-only kind reclassification
- feat(guidance): teach the constraint-vs-convention discriminator at write time
- feat(recall): optional kind filter applied inside the candidate queries
- feat(briefing): CONVENTION section + convention_max_full; constraint_max_full default 4
- feat(memory): add convention kind end-to-end (enum, console, gardener prompt, docs)
- feat(briefing): prompt-matched RELEVANT section in subagent briefings
- feat(briefing): subagent footer, spawn-prompt capture, funnel-by-surface split
- feat(hooks): brief Claude Code Task subagents via SubagentStart
- feat(briefing): clip findings at word boundaries and refresh docs
- feat(briefing): promote mishap-referenced constraints in the full tier
- feat(briefing): tier constraints into top-K full lines plus compact tail
- feat(briefing): reorder body, add constraint_max_full, link mishaps
- feat(docs): redesign figure connectors
- feat(console): tighten Interactions controls and event inspector
- feat(console): redesign event review workspace
- feat(console): make Interactions a clean live feed
- feat(console): add an agent-reported mishaps rail to the Overview
- feat(site): expose WebMCP tools to browser agents
- feat(docsgen): publish the Agent Skills discovery index
- feat(a2a): serve a real A2A surface and publish its agent card
- feat(docsgen): emit /auth.md documenting the local-first auth model
- feat(docsgen): emit the MCP Server Card at /.well-known/mcp/server-card.json
- feat(docsgen): emit a site-root index.md twin; drop the Content-Type edge rule
- feat(docsgen): emit a markdown twin per page for text/markdown negotiation
- feat(docsgen): emit /.well-known/api-catalog at the site root

### Fixes

- fix(docs): preserve flow label casing
- fix(installer): report only actual changes
- fix(console): stabilize interaction model attribution

## v0.4.3 {#v0-4-3}

Released 2026-07-22 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.4.3)

### Features

- feat(console): per-gate readiness in the utility activation table

### Other

- refactor(console): merge the utility activation table into the Closed loop group

## v0.4.2 {#v0-4-2}

Released 2026-07-22 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.4.2)

### Features

- feat(sessions): self-reported mishaps at session_end
- feat(gardener): add the tool-error pass -- recurring agent-facing errors become fix tasks
- feat(console): make the interaction-volume histogram interactive

### Fixes

- fix(console): locate timeline events in place

## v0.4.1 {#v0-4-1}

Released 2026-07-21 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.4.1)

### Features

- feat(console): semantic index and storage panel on Settings
- feat(gardener): recall-miss ledger and memory-wanted proposals
- feat(console): session review workspace and recency-first list order
- feat(retrieval): closed-loop utility ranking for briefings and recall
- feat(console): redesign the login page around getting in, not locked out
- feat(console): show memory churn on the Retrieval report
- feat(mcp): add registry server.json for io.github.0spoon/seamless

## v0.4.0 {#v0-4-0}

Released 2026-07-21 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.4.0)

### Features

- feat(site): expand the README as the highest-authority search surface
- feat(release): publish a Homebrew cask to 0spoon/homebrew-tap on release
- feat(site): crawlable /scenarios/ pages and four definition pages
- feat(site): release-time /docs/changelog/ generated from git tags
- feat(site): hand-written /compare/ hub and the Agent Teams FAQ entry
- feat(site): answer-first openers for the pages that map to a real query
- feat(site): one honest MCP-clients guide for Cursor, Cline, Windsurf and Zed
- feat(site): manual IndexNow ping and a make metrics target
- feat(site): crawlable scenes, authored section landings, unpublish SITE.md
- feat(site): JSON-LD structured data plus the gates that keep it honest
- feat(docsgen): add canonical, robots and Open Graph head tags to every docs page
- feat(console): redesign Retrieval as a four-zone circulation report

## v0.3.9 {#v0-3-9}

Released 2026-07-21 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.9)

### Features

- feat(console): share one in-place navigation client across data refreshes
- feat(sessions): harvest real model token usage on both Claude Code and Codex
- feat(install): wire the chat surface into client detection and selection
- feat(doctor): verify the Claude app chat surface's desktop-config registration
- feat(seamlessd): register the chat-surface MCP bridge in claude_desktop_config.json
- feat(doctor,store): verify the Claude app's embedded code surface
- feat(console): edit project families in Settings and cost the reach card
- feat(console): redesign overview, gardener, and settings surfaces
- feat(docsgen): emit llms.txt and llms-full.txt at the site root
- feat(docsgen): generate and gate sitemap.xml and robots.txt at the site root
- feat(console): give the operational explorers shared orientation chrome
- feat(console): add a reader nav strip and shared library chrome
- feat(search): add time windows, sorts, and a unified console search stream
- feat(search): floor semantic-only hits and show similarity in console search
- feat: add Codex shared-host diagnostics
- feat(console): add Labs and Trials library screens
- feat(seam): add seam version, reporting the running daemon's version

### Fixes

- fix(docsgen): exempt author-written dates from the build-timestamp tripwire
- fix(mcp): stop lab-only bindings from masquerading as session bindings

## v0.3.8 {#v0-3-8}

Released 2026-07-20 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.8)

### Features

- feat(install): confirm client choice, drop the silent Claude fallback

### Fixes

- fix(test): remove load-sensitive 5s deadline from fake-codex tests

## v0.3.7 {#v0-3-7}

Released 2026-07-20 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.7)

### Features

- feat(hooks): integrate Codex subagent lifecycle
- feat(install): detect the agent clients instead of defaulting to Claude
- feat(hooks): cap Codex hook context below the output spill threshold
- feat(codex): add MCP instructions and native skills
- feat(console): harness and model attribution pills on sessions, memories, notes, and the feed
- feat(security): land the 2026-07 audit hardening (M1-M3 and low-severity fixes)
- feat(attribution): record which model produced each memory, note, and session

### Fixes

- fix(release): ignore the CI-generated sigs/ so goreleaser sees a clean tree
- fix(seamlessd): keep MCP stderr out of JSON
- fix(seamlessd): report Codex operational truth
- fix(scripts): pin --client claude in the scene-fixture harness
- fix(seamlessd): reconcile Codex MCP registration
- fix(skills): stop double-wrapping the mkdir error
- fix(hooks): prepare the plan-context block once for telemetry and response
- fix(console): resolve provenance stamps by shape and stop pinning mutable session fields
- fix(doctor): judge hooks against the recorded seam path, not the running binary's sibling
- fix(install): warn when a pinned 0.3.3-0.3.6 binary bundles no skills
- fix(install): pass an explicit --client from make install
- fix(scripts): reject traversal skill names before rm -rf
- fix(skills): restore disable-model-invocation on seam-onboard
- fix(hooks): adopt hand-written tilde hook commands instead of duplicating
- fix(seamlessd): degrade skill install instead of aborting install-hooks
- fix(hooks): keep item ids on a truncated Codex injection
- fix(install): probe for --client before passing it to a pinned binary
- fix(hooks): reject an unknown hook client instead of defaulting to Claude
- fix(hooks): classify definitions exactly
- fix(sessions): key ambient sessions by full external identity
- fix(release): pin cosign to the v2 line so the sign stage survives cosign v3

## v0.3.6 {#v0-3-6}

Released 2026-07-19 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.6)

### Features

- feat(update): seamlessd update + --check, with make update parity
- feat(console): Plans joins the library layout
- feat(console): unify Notes, Memories, and Tasks into a library layout

### Fixes

- fix(version): derive build version from git tag so make and releases agree
- fix(console): per-project Retrieval trend on the project Overview

## v0.3.5 {#v0-3-5}

Released 2026-07-19 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.5)

### Features

- feat(service): cross-OS `seamlessd start|stop|restart|status` + make parity
- feat(uninstall): cross-OS `seamlessd uninstall` + make delegation

## v0.3.4 {#v0-3-4}

Released 2026-07-18 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.4)

### Features

- feat(install): structured, colored, terser install output
- feat(install): interactive client picker + graceful non-Claude-Code doctor

## v0.3.3 {#v0-3-3}

Released 2026-07-18 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.3)

### Features

- feat(codex): install-hooks --client codex profile + doctor awareness
- feat(codex): Stop-hook lifecycle -- heartbeat + rollout tail-harvest
- feat(codex): per-client hook payload adapter and --client plumbing
- feat(codex): client-derived ambient identity (cc/ vs cx/) and external session id
- feat(codex): add seam mcp-proxy stdio&lt;-&gt;HTTP bridge for stdio-only MCP clients

### Fixes

- fix(site): cache-bust static assets so deploys stop serving stale CSS/JS

## v0.3.2 {#v0-3-2}

Released 2026-07-18 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.2)

### Features

- feat(site): OS-aware install command on the landing page
- feat(windows): package, install, and supervise Seamless on Windows
- feat(hooks): emit exec-form command hooks + seam hook --config

### Other

- refactor(demoseed): extract seeding core into internal/demokit; thin CLI

## v0.3.1 {#v0-3-1}

Released 2026-07-17 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.1)

### Features

- feat(marketing): make the demo scenes a self-running reel; refresh wordmark
- feat(marketing): ship the animated with/without terminal scenes section
- feat(mcp,briefing,skills): batch+enrich missing-required errors, reconcile kind/type guidance
- feat(demoseed,marketing): record with/without terminal scenes + distill all four into scenes.js
- feat(mcp): resolve task-claim identity from the ambient, not the transport binding
- feat(skills): ship /seam-research as a repo-maintained skill
- feat(installer): bundle the /seam-onboard skill in the release
- feat(mcp,docs): make repo auto-mapping visible; map-repo is override-only
- feat(demoseed): scene fixture + dual harness for terminal captures
- feat(briefing): clean truncation, subset header, drop no-summary findings
- feat(gardener,briefing): age out gateless stage memories
- feat(site): swap the console sketch for real captures from a seeded demo
- feat(install): one-command curl | sh installer for macOS and Linux

### Fixes

- fix(console): window plans on composition activity, lead with the all-time total
- fix(console): stop the bare .plan rule leaking onto the Plans filter chip

### Other

- build: gate the landing page against the installer and the CLI

## v0.3.0 {#v0-3-0}

Released 2026-07-16 - [downloads and notes](https://github.com/0spoon/seamless/releases/tag/v0.3.0)

The first public release of Seamless.
