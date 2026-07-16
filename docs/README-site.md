# thereisnospoon.org -- landing page + docs site

This directory is the Seamless public site, served by GitHub Pages: a
hand-written landing page at `/`, and the generated documentation site at
`/docs/`.

## Contents

```
index.html              the landing page (single file, no build step)
CNAME                   custom domain: thereisnospoon.org
.nojekyll               disable Jekyll (it would skip our _-prefixed paths)
static/site.css         design system, mirrored from internal/console tokens
static/site.js          theme toggle, copy buttons, scroll reveals (no deps)
static/favicon.svg      the 0spoon mark (an empty set)
static/shots/           console screenshots, dark + light (see below)
static/og.png           1200x630 social preview card
static/og-source.html   source for og.png (see below)
static/fonts/           self-hosted variable woff2 (OFL, see OFL-NOTICE.txt)
docs/                   GENERATED docs site -- do not edit (see below)
```

Fully self-contained: no CDNs, no webfont requests, no analytics, no cookies.
Light + dark themes (follows the OS; the toggle stores an override). The docs
share this design system and the same `localStorage("theme")` key, so a theme
chosen on either surface holds across both.

## The docs site is generated

**Never edit `docs/docs/` by hand.** It is build output, committed so GitHub
Pages can serve it without a CI build step:

```
docs-src/           markdown sources + nav.yaml (the information architecture)
cmd/docsgen/        the generator: templates, assets, and the two code generators
docs/docs/          generated HTML (committed; every file carries a marker)
```

```bash
make docs           # regenerate docs/docs/ from docs-src/
make docs-serve     # regenerate and serve at 127.0.0.1:8899/docs/
make docs-check     # fail if the committed output is stale (part of `make check`)
```

**The drift rule:** because the output is committed and `make check` runs
`docs-check`, any change to `docs-src/` must be followed by `make docs` and the
result committed in the same change, or the build goes red. That applies to
changes in the *code* too: the MCP tool reference is generated from
`mcp.Catalog()` and the configuration reference from `config.Defaults()`, so
adding a tool or a config key makes the committed docs stale by design -- that is
the mechanism that stops the reference from quietly lying.

Upgrading chroma, goldmark, or Go can shift the generated HTML for the same
reason. Regenerate in the same PR as the upgrade. Never auto-regenerate in CI:
the point of committing the output is that a human saw it.

## Preview locally

```
make docs-serve     # landing page at /, docs at /docs/
```

Or any static file server pointed at this directory:

```
cd docs && python3 -m http.server 8899
```

then open http://127.0.0.1:8899/.

## Go live (manual, owner-gated)

1. GitHub repo Settings -> Pages -> Source: `main` branch, `/docs` folder.
2. DNS for thereisnospoon.org: apex A records to GitHub Pages IPs
   (185.199.108.153 .. 185.199.111.153) + AAAA if desired; `www` CNAME to
   `0spoon.github.io` (optional). The `CNAME` file here is already in place.
3. In Pages settings, set the custom domain and enable Enforce HTTPS once the
   cert is issued.

## Regenerate the OG card

```
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless \
  --disable-gpu --screenshot=docs/static/og.png --window-size=1200,630 \
  --hide-scrollbars 'http://127.0.0.1:8899/static/og-source.html'
```

## Regenerate the console shots

`static/shots/` holds the console screenshots (5 pages x dark/light, WebP).
They come from a THROWAWAY instance seeded with fictional data by
`cmd/demoseed` -- never from a live data dir. To re-capture after a console
change:

```bash
# 1. seed a throwaway data dir (numbers are tuned; see cmd/demoseed/data.go)
go run ./cmd/demoseed -data /tmp/seamless-demo

# 2. serve it on a port that is NOT your live daemon
SEAMLESS_DATA_DIR=/tmp/seamless-demo SEAMLESS_ADDR=127.0.0.1:8090 \
  SEAMLESS_MCP_API_KEY=<any key> ./bin/seamlessd serve

# 3. capture both themes at 1440x900 @2x (Playwright driving installed Chrome)
SEAMLESS_SHOT_BASE=http://127.0.0.1:8090 SEAMLESS_MCP_API_KEY=<same key> \
  node scripts/console-shots.js /tmp/shots

# 4. convert into place
for f in /tmp/shots/*.png; do
  cwebp -q 84 "$f" -o "docs/static/shots/$(basename "${f%.png}").webp"
done
```

Capture within ~an hour of seeding: the demo's "live" sessions and leases are
anchored to the seeding time and go stale on screen after that.

## Pending (owner approval required)

- Spot-check on a real phone. Narrow widths are no longer unexercised: the page
  was swept at 320/360/390/414/600/768/900/1024/1280/1440 under Chrome device
  emulation and has no horizontal overflow at any of them. That sweep caught a
  real bug (the hero and quickstart grids each declared a bare `1fr`, whose
  min-content floor was the nowrap install command -- 157px of overflow on a
  390px phone); both now use `minmax(0, 1fr)`. A real handset would still add
  touch-target and font-rendering confidence that emulation can't.
