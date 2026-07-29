# Glama directory image

The container the [Glama](https://glama.ai) MCP directory builds to introspect
Seamless. It exists for one reason: Glama will not assign a **quality score**
until the server has a *Glama release*, and that score is what
[punkpeye/awesome-mcp-servers](https://github.com/punkpeye/awesome-mcp-servers)
requires before it will list a server.

This is not a supported way to run Seamless. Seamless is a local-first daemon
that lives beside your agent and owns `~/.seamless`; in the container the data
dir is an empty volume that dies with the process, so there is no memory to
carry across sessions and no repo to map. It starts, it introspects, that's it.

## How it works

Glama's runner speaks stdio. Seamless serves MCP over streamable HTTP, so the
entrypoint starts `seamlessd serve` on container loopback and execs
`seam mcp-proxy`, the same stdio bridge the Codex and Claude Desktop
registrations use. `initialize` and `tools/list` then answer normally.

Two things the entrypoint is careful about:

- **stdout belongs to MCP.** The daemon's stdout is redirected to stderr; a
  stray line on stdout would corrupt the client's frame stream.
- **No config file ships in the image**, so settings arrive as `SEAMLESS_*` env
  (env > file > defaults) and `config.EnsureAPIKey`'s first-run generation never
  fires -- the entrypoint mints a per-start key for the loopback-only daemon.

Embeddings and gardener digests log as disabled (no LLM key). That is expected
and does not affect introspection.

## Build and smoke-test locally

Build context is the repository root:

```bash
docker build -f deploy/glama/Dockerfile -t seamless-mcp .

printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | docker run --rm -i seamless-mcp
```

Expect two JSON-RPC replies on stdout: the `initialize` result naming Seamless,
and a `tools/list` result carrying the full tool catalog (`mcp.ToolCount`).

## Publishing a Glama release

The score checklist lives at
<https://glama.ai/mcp/servers/0spoon/seamless/score>. A Glama release is not a
GitHub release:

1. **Claim the server** -- authenticate on Glama with GitHub as a maintainer
   listed in the repo's `glama.json`. This also clears the separate
   "author not verified" check.
2. **Dockerfile admin page** -- paste this Dockerfile into the build spec and
   click **Deploy**. It is self-contained on purpose: the entrypoint is written
   inline rather than COPYd, so the pasted spec never depends on a file that
   exists only in a working tree, and the repo and Glama copies cannot drift.
3. Once the build test passes, click **Make Release**, enter a version, publish.

Server Coherence and Tool Definition Quality only score after a release exists,
and together they are the quality score. Tool Definition Quality is the heavy
term: it grades every tool's description, and the server score weights the
*worst* tool heavily, so a single thin description drags the whole result down.
