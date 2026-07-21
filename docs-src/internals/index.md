---
title: Internals
description: For contributors - how the Go packages layer, what the check gate enforces, and the domain invariants that plausible-looking code breaks.
---

These pages are for people changing Seamless rather than running it. They
document the things a contributor cannot recover from the code alone: why the
packages layer the way they do, what the build gate is actually defending, and
the invariants that look like implementation details until a refactor quietly
breaks one.

[Architecture](/internals/architecture/) is the map - what each package owns,
the dependency direction between them, and two data-flow traces followed
through the real code: a memory write from MCP call to markdown file, and a
briefing assembly from session start to injected text. It also lists the
things Seamless deliberately does not have, which explains more design
decisions than what it does.

[Contributing](/internals/contributing/) is the workflow page: the make
targets, what `make check` gates and in what order, the conventions that are
enforced rather than suggested, the forbidden APIs, and the three places a new
MCP tool must be wired before the build goes green.

[Domain invariants](/internals/invariants/) is the page to read before
touching lifecycle, scope, or search code. Each entry is a rule that
reasonable-looking code has broken or would break - supersession ordering,
scope resolution, FTS and LIKE escaping, LLM degradation - stated with the
failure it prevents, so you can tell when a change is about to relearn one the
hard way.
