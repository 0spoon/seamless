# Glama directory image

What the [Glama](https://glama.ai) MCP directory builds to introspect Seamless.
It exists for one reason: Glama assigns no **quality score** until the server
has a *Glama release*, and that score is what
[punkpeye/awesome-mcp-servers](https://github.com/punkpeye/awesome-mcp-servers)
requires before it will list a server.

This is not a supported way to run Seamless. Seamless is a local-first daemon
that lives beside your agent and owns `~/.seamless`; in the container the data
dir is empty and dies with the process, so there is no memory to carry across
sessions and no repo to map. It starts, it introspects, that's it.

## Shape

Glama does not accept a Dockerfile. Its admin page **generates** one from a
form: `debian:trixie-slim`, node, python, a `git clone` of this repo into
`/app`, then whatever the form's *build steps* and *CMD arguments* say. Its CMD
wraps the server in Glama's own stdio proxy, `mcp-proxy -- <your command>`.

So the two moving parts live here as scripts the form points at:

- `build.sh` -- installs the Go toolchain (version tracked from `go.mod`),
  builds `seamlessd` and `seam` into `/usr/local/bin`, installs the entrypoint,
  then drops the toolchain and both Go caches (~1GB -> ~270MB).
- `entrypoint.sh` -- starts `seamlessd serve` on container loopback and execs
  `seam mcp-proxy`, the stdio bridge Codex and Claude Desktop already use, so
  Glama's proxy gets the stdio child it expects.

`Dockerfile` is a **local mirror** of that generated image, not something Glama
reads. It is how you verify a change before clicking Build over there.

Two things `entrypoint.sh` is careful about:

- **stdout belongs to MCP.** The daemon's stdout is redirected to stderr; a
  stray line on stdout would corrupt the client's frame stream.
- **No config file exists in the clone**, so settings arrive as `SEAMLESS_*`
  env (env > file > defaults) and `config.EnsureAPIKey`'s first-run generation
  never fires -- the entrypoint mints a per-start key for the loopback-only
  daemon.

Embeddings and gardener digests log as disabled (no LLM key). Expected, and it
does not affect introspection.

## Verify locally before you touch the admin page

```bash
docker build -f deploy/glama/Dockerfile -t seamless-mcp .

printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | docker run --rm -i seamless-mcp
```

Expect two JSON-RPC replies on stdout: an `initialize` result naming Seamless
at the `server.json` version, and a `tools/list` carrying the full catalog
(`mcp.ToolCount`).

## Admin form values

<https://glama.ai/mcp/servers/0spoon/seamless/admin> -> Dockerfile tab. Only
three fields differ from their defaults:

| Field | Value |
|---|---|
| Base image | `debian:trixie-slim` (default) |
| Node.js / Python version | defaults; neither is used |
| **Build steps** | `["sh deploy/glama/build.sh"]` |
| **CMD arguments** | `["/usr/local/bin/seamless-mcp"]` |
| Environment variables JSON schema | default -- the server needs none |
| **Placeholder parameters** | `{}` -- it starts with no credentials |
| Pinned commit SHA | empty (build head); press *sync* after pushing |

## Publishing a release

A Glama release is not a GitHub release:

1. **Claim the server** -- authenticate on Glama with GitHub as a maintainer
   listed in `glama.json`. Also clears the separate "author not verified" check.
2. **Build** -- the build test. Fix anything it reports before releasing.
3. **Build & Release** -- enter a version matching `server.json`, publish.

Server Coherence and Tool Definition Quality only score once a release exists,
and together they are the quality score. Tool Definition Quality is the heavy
term: it grades every tool's description, and the server score weights the
*worst* tool heavily, so a single thin description drags the whole result down.
