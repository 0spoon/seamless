# auth.md

How authentication works for Seamless. Written for AI agents (and the humans
running them) deciding how to connect.

## This site has no protected APIs

Everything served on this domain is public and unauthenticated: the landing
page, the docs, `llms.txt`, the API catalog, and the MCP Server Card. There is
no hosted API and no authorization server here, which is why this domain
deliberately publishes no `/.well-known/openid-configuration` and no
`/.well-known/oauth-authorization-server`: OAuth discovery metadata would
advertise endpoints that do not exist.

## The real API is per-install, on localhost

Seamless is local-first. The MCP endpoint runs on each machine that installs
it -- `http://127.0.0.1:8081/api/mcp` (streamable HTTP), served by the
`seamlessd` daemon, bound to localhost. There is no hosted remote, no sign-up,
and no registration endpoint on this domain.

## Supported authentication method

One method: a static bearer key, unique to each install.

- Every MCP request carries `Authorization: Bearer <key>`.
- The daemon compares the key in constant time and rejects requests without it.
- The trust boundary is the machine: the daemon binds `127.0.0.1` only.

## How credentials are provisioned and used

- The installer (`curl -fsSL https://thereisnospoon.org/install | sh`)
  generates the key on first run and stores it as `mcp.api_key` in
  `~/.config/seamless/seamless.yaml` (mode 0600). The `SEAMLESS_MCP_API_KEY`
  environment variable overrides the file.
- The installer registers the MCP server with the detected agent clients
  (Claude Code, Codex, the Claude desktop app). Registered clients read the
  key from the local config at connection time -- directly or via the
  `seam mcp-proxy` stdio bridge -- so the credential is never copied into
  client configs or argv.
- To rotate the key: edit `mcp.api_key`, restart the daemon. Clients pick up
  the new key on their next connection.

## No OAuth, by design

There is no OAuth flow, no dynamic client registration, no token endpoint,
and no JWKS. If Seamless ever grows a hosted multi-user surface, discovery
metadata will appear at the standard well-known paths named above.
