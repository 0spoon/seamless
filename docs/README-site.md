# thereisnospoon.org -- landing page

This directory is the Seamless marketing site, served by GitHub Pages.

## Contents

```
index.html              the page (single file, no build step)
CNAME                   custom domain: thereisnospoon.org
static/site.css         design system, mirrored from internal/console tokens
static/site.js          theme toggle, copy buttons, scroll reveals (no deps)
static/favicon.svg      the 0spoon mark (an empty set)
static/og.png           1200x630 social preview card
static/og-source.html   source for og.png (see below)
static/fonts/           self-hosted variable woff2 (OFL, see OFL-NOTICE.txt)
```

Fully self-contained: no CDNs, no webfont requests, no analytics, no cookies.
Light + dark themes (follows the OS; the toggle stores an override).

## Preview locally

Any static file server pointed at this directory works, e.g.:

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

## Pending (owner approval required)

- Real console screenshots / demo GIF to replace or accompany the CSS console
  sketch (needs a sanitized demo instance; see memory
  `throwaway-console-only-on-request`).
- Verify rendering on a real phone (responsive CSS uses standard breakpoints
  but was only exercised at desktop widths).
