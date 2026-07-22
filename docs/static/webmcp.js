/* thereisnospoon.org -- WebMCP tools (webmachinelearning.github.io/webmcp).
   Registers read-only tools with the browser's model context, so an agent
   driving the browser can search the docs, read any page as markdown, and
   find the machine-readable endpoints without scraping HTML. Loaded by every
   page (landing, /compare/, scenarios, docs); a silent no-op in browsers
   without a model context. No dependencies, no state; the only network calls
   are same-site fetches made when an agent invokes a tool. */
(function () {
  "use strict";

  /* Chrome's origin-trial surface hangs off navigator, the spec draft off
     document; take whichever exists. */
  var mc = navigator.modelContext || document.modelContext;
  if (!mc) return;

  /* Site root, derived from this script's own URL so the tools work wherever
     the site is mounted: the apex, the project-pages fallback, docs-serve. */
  var script = document.currentScript;
  if (!script || !script.src) return;
  var root = script.src.slice(0, script.src.lastIndexOf("static/webmcp.js"));

  function result(s) {
    return { content: [{ type: "text", text: s }] };
  }

  function fetchText(url) {
    return fetch(url).then(function (r) {
      if (!r.ok) throw new Error("HTTP " + r.status + " for " + url);
      return r.text();
    });
  }

  /* ------------------------------------------------------------- search */
  var indexPromise = null;
  function loadIndex() {
    if (!indexPromise) {
      indexPromise = fetch(root + "docs/static/search-index.json").then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status + " loading the search index");
        return r.json();
      });
    }
    return indexPromise;
  }

  /* Same weights as the docs search box (docs.js), so an agent and a human
     typing the same query see the same ranking. Every term must appear. */
  function score(doc, terms) {
    var title = doc.title.toLowerCase();
    var section = doc.section.toLowerCase();
    var headings = doc.headings.join(" ").toLowerCase();
    var text = doc.text.toLowerCase();
    var total = 0;
    for (var i = 0; i < terms.length; i++) {
      var t = terms[i];
      var hit = 0;
      if (title.indexOf(t) >= 0) hit += title === t ? 120 : 60;
      if (section.indexOf(t) >= 0) hit += 12;
      if (headings.indexOf(t) >= 0) hit += 25;
      if (text.indexOf(t) >= 0) hit += 6;
      if (!hit) return 0;
      total += hit;
    }
    return total;
  }

  function snippet(doc, terms) {
    var at = doc.text.toLowerCase().indexOf(terms[0]);
    if (at < 0) at = 0;
    var start = Math.max(0, at - 60);
    var cut = doc.text.slice(start, start + 220).trim();
    return (start > 0 ? "..." : "") + cut + (start + 220 < doc.text.length ? "..." : "");
  }

  /* ---------------------------------------------------------- read_page */
  /* Root-absolute same-site path for a page, or null. Accepts a full URL on
     this site or a path; drops query and fragment; refuses anything that
     could leave the site. */
  function normalizePath(p) {
    p = String(p == null ? "" : p).trim();
    if (!p) return null;
    if (p.indexOf(root) === 0) p = "/" + p.slice(root.length);
    if (/^(https?:)?\/\//i.test(p)) return null; /* another origin */
    p = p.replace(/[?#].*$/, "");
    if (p.indexOf("..") >= 0) return null;
    if (p.charAt(0) !== "/") p = "/" + p;
    return p;
  }

  var tools = [
    {
      name: "search_docs",
      title: "Search the Seamless docs",
      description:
        "Full-text search over the Seamless documentation at " + root + "docs/. " +
        "Returns the top matching pages with URL, section, and a snippet. " +
        "Read a result with read_page.",
      inputSchema: {
        type: "object",
        properties: {
          query: {
            type: "string",
            description: "Search terms; pages matching every term rank first"
          }
        },
        required: ["query"]
      },
      annotations: { readOnlyHint: true },
      execute: function (input) {
        var q = String(input && input.query || "").trim().toLowerCase();
        if (!q) return Promise.resolve(result("Empty query."));
        var terms = q.split(/\s+/);
        return loadIndex().then(function (docs) {
          var hits = [];
          docs.forEach(function (doc) {
            var s = score(doc, terms);
            if (s > 0) hits.push({ doc: doc, score: s });
          });
          hits.sort(function (a, b) { return b.score - a.score; });
          hits = hits.slice(0, 5);
          if (!hits.length) return result("No pages match [" + q + "].");
          var lines = hits.map(function (h) {
            return "- " + h.doc.title + " (" + h.doc.section + ")\n  " +
              root + "docs/" + h.doc.url + "\n  " + snippet(h.doc, terms);
          });
          return result(lines.join("\n"));
        });
      }
    },
    {
      name: "read_page",
      title: "Read a page as markdown",
      description:
        "Read any page of this site as markdown. Takes a path (/docs/quickstart/) " +
        "or a full URL on this site; every page serves a markdown twin next to " +
        "its HTML. Plain files (/llms.txt, /auth.md) are returned as-is.",
      inputSchema: {
        type: "object",
        properties: {
          path: {
            type: "string",
            description: "Page path or same-site URL, e.g. /docs/concepts/memory/"
          }
        },
        required: ["path"]
      },
      annotations: { readOnlyHint: true },
      execute: function (input) {
        var p = normalizePath(input && input.path);
        if (!p) return Promise.resolve(result("Not a path on this site."));
        /* A page is a directory URL whose markdown twin is index.md; a plain
           text-ish file is fetched as-is. */
        var last = p.slice(p.lastIndexOf("/") + 1);
        if (last && !/\.(md|txt|json)$/i.test(last)) p += "/";
        var url = root + p.slice(1) + (p.charAt(p.length - 1) === "/" ? "index.md" : "");
        return fetchText(url).then(result, function () {
          return result("No markdown at " + url + ". Use search_docs to find pages.");
        });
      }
    },
    {
      name: "list_agent_resources",
      title: "List machine-readable endpoints",
      description:
        "The machine-readable endpoints this site publishes for agents: llms.txt, " +
        "the MCP server card, the A2A agent card, the Agent Skills index, the " +
        "API catalog, the auth model, and the installers.",
      inputSchema: { type: "object", properties: {} },
      annotations: { readOnlyHint: true },
      execute: function () {
        var r = [
          { url: root + "llms.txt", what: "index of the docs for LLMs (llmstxt.org)" },
          { url: root + "llms-full.txt", what: "the full docs in one markdown file" },
          { url: root + "docs/", what: "the docs; every page serves a markdown twin at <page>index.md" },
          { url: root + "auth.md", what: "the auth model: this site is public, the product is local-first (no OAuth by design)" },
          { url: root + ".well-known/mcp/server-card.json", what: "MCP Server Card for the seamlessd MCP server agents run locally" },
          { url: root + ".well-known/agent-card.json", what: "A2A Agent Card; each install's daemon serves the same card live" },
          { url: root + ".well-known/agent-skills/index.json", what: "Agent Skills discovery index (SKILL.md files)" },
          { url: root + ".well-known/api-catalog", what: "RFC 9727 API catalog (linkset)" },
          { url: root + "install", what: "macOS/Linux install: curl -fsSL https://thereisnospoon.org/install | sh" },
          { url: root + "install.ps1", what: "Windows install: irm https://thereisnospoon.org/install.ps1 | iex" },
          { url: "https://github.com/0spoon/seamless", what: "the repository" }
        ];
        return Promise.resolve(result(JSON.stringify(r, null, 2)));
      }
    }
  ];

  if (typeof mc.provideContext === "function") {
    mc.provideContext({ tools: tools });
  } else if (typeof mc.registerTool === "function") {
    tools.forEach(function (t) { mc.registerTool(t); });
  }
})();
