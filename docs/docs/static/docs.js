/* thereisnospoon.org/docs/ -- sidebar, copy buttons, TOC scrollspy, client search.
   No dependencies, no network beyond one lazy fetch of the local search index,
   no state beyond localStorage("theme") -- shared with the landing page. */
(function () {
  "use strict";

  var root = document.documentElement;
  /* Every href is relative, so the docs work at thereisnospoon.org/docs/, at the
     project-pages fallback, and under `make docs-serve`. The generator stamps the
     prefix each page needs; JS-built links must use the same one. */
  var docsRoot = document.body.dataset.docsRoot || "";

  /* ---------------------------------------------------------------- theme */
  var toggle = document.querySelector(".theme-toggle");
  function effectiveTheme() {
    if (root.dataset.theme) return root.dataset.theme;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  if (toggle) {
    toggle.addEventListener("click", function () {
      var next = effectiveTheme() === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      try { localStorage.setItem("theme", next); } catch (e) { /* private mode */ }
    });
  }

  /* -------------------------------------------------------------- sidebar */
  var sidebar = document.getElementById("sidebar");
  var navToggle = document.querySelector(".nav-toggle");
  if (navToggle && sidebar) {
    navToggle.addEventListener("click", function () {
      var open = sidebar.classList.toggle("open");
      navToggle.setAttribute("aria-expanded", open ? "true" : "false");
    });
    document.addEventListener("click", function (ev) {
      if (!sidebar.classList.contains("open")) return;
      if (sidebar.contains(ev.target) || navToggle.contains(ev.target)) return;
      sidebar.classList.remove("open");
      navToggle.setAttribute("aria-expanded", "false");
    });
  }
  /* Keep the current page visible in a long sidebar without scrolling the page. */
  var current = sidebar && sidebar.querySelector("a.current");
  if (current && sidebar.scrollHeight > sidebar.clientHeight) {
    var top = current.offsetTop - sidebar.clientHeight / 2;
    sidebar.scrollTop = top > 0 ? top : 0;
  }

  /* ------------------------------------------------------------ copy code */
  document.querySelectorAll(".prose pre").forEach(function (pre) {
    var btn = document.createElement("button");
    btn.className = "copy-code";
    btn.type = "button";
    btn.textContent = "copy";
    btn.setAttribute("aria-label", "Copy code to clipboard");
    btn.addEventListener("click", function () {
      var code = pre.querySelector("code");
      navigator.clipboard.writeText(code ? code.innerText : pre.innerText).then(function () {
        btn.textContent = "copied";
        btn.classList.add("ok");
        setTimeout(function () {
          btn.textContent = "copy";
          btn.classList.remove("ok");
        }, 1600);
      });
    });
    pre.appendChild(btn);
  });

  /* ----------------------------------------------------- scrollable tables */
  /* A wide table scrolls inside its .table-wrap, and a box you can only scroll
     with a pointer is unreachable by keyboard. Make the overflowing ones focusable
     -- only those, or every table on the page becomes a tab stop for nothing. The
     set changes with the viewport, so re-check on resize. */
  var wraps = Array.prototype.slice.call(document.querySelectorAll(".table-wrap"));
  if (wraps.length) {
    var syncWraps = function () {
      wraps.forEach(function (wrap) {
        if (wrap.scrollWidth > wrap.clientWidth + 1) {
          wrap.setAttribute("tabindex", "0");
          wrap.setAttribute("role", "region");
          wrap.setAttribute("aria-label", "Table, scrollable");
        } else {
          wrap.removeAttribute("tabindex");
          wrap.removeAttribute("role");
          wrap.removeAttribute("aria-label");
        }
      });
    };
    syncWraps();
    var resizeTimer;
    window.addEventListener("resize", function () {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(syncWraps, 150);
    });
  }

  /* ------------------------------------------------------------ scrollspy */
  var tocLinks = Array.prototype.slice.call(document.querySelectorAll(".docs-toc a"));
  if (tocLinks.length && "IntersectionObserver" in window) {
    var byId = {};
    tocLinks.forEach(function (a) { byId[a.getAttribute("href").slice(1)] = a; });
    var visible = {};
    var spy = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { visible[e.target.id] = e.isIntersecting; });
      var chosen = null;
      /* Highest heading currently on screen wins; document order == TOC order. */
      tocLinks.forEach(function (a) {
        var id = a.getAttribute("href").slice(1);
        if (!chosen && visible[id]) chosen = a;
      });
      tocLinks.forEach(function (a) { a.classList.toggle("active", a === chosen); });
    }, { rootMargin: "-70px 0px -70% 0px" });
    Object.keys(byId).forEach(function (id) {
      var el = document.getElementById(id);
      if (el) spy.observe(el);
    });
  }

  /* ------------------------------------------------------- context picker */
  /* The head script seeded data-os / data-clients on <html> pre-paint (UA
     detect, localStorage, or ?os=&client= query params). The bar's buttons
     rewrite that state for the whole page and persist it; block visibility
     is pure CSS off the two attributes. */
  var ctxBar = document.querySelector(".ctx-bar");
  if (ctxBar) {
    var OS_LABELS = { macos: "macOS", linux: "Linux", windows: "Windows" };
    var CLIENT_ORDER = ["claude", "claude-desktop", "codex"];
    var CLIENT_LABELS = { claude: "Claude Code", "claude-desktop": "Claude app chat", codex: "Codex" };
    var ctxStatus = ctxBar.querySelector(".ctx-status");

    var selectedClients = function () {
      return (root.dataset.clients || "").split(" ").filter(Boolean);
    };
    var syncCtx = function () {
      var os = root.dataset.os || "all";
      ctxBar.querySelectorAll("[data-ctx-os-pick]").forEach(function (btn) {
        btn.setAttribute("aria-pressed", btn.dataset.ctxOsPick === os ? "true" : "false");
      });
      var picked = selectedClients();
      ctxBar.querySelectorAll("[data-ctx-client-pick]").forEach(function (btn) {
        var v = btn.dataset.ctxClientPick;
        var on = v === "all" ? picked.length === 0 : picked.indexOf(v) >= 0;
        btn.setAttribute("aria-pressed", on ? "true" : "false");
      });
      if (ctxStatus) {
        ctxStatus.textContent = "Showing steps for " + (OS_LABELS[os] || "every OS") + ", " +
          (picked.length ? picked.map(function (v) { return CLIENT_LABELS[v]; }).join(" + ") : "every client") + ".";
      }
    };
    ctxBar.addEventListener("click", function (ev) {
      var osBtn = ev.target.closest("[data-ctx-os-pick]");
      var clientBtn = ev.target.closest("[data-ctx-client-pick]");
      if (osBtn) {
        root.dataset.os = osBtn.dataset.ctxOsPick;
        try { localStorage.setItem("os", root.dataset.os); } catch (e) { /* private mode */ }
      } else if (clientBtn) {
        var v = clientBtn.dataset.ctxClientPick;
        var picked = v === "all" ? [] : selectedClients();
        if (v !== "all") {
          var at = picked.indexOf(v);
          if (at >= 0) picked.splice(at, 1); else picked.push(v);
          /* Selecting all three is "All": drop the filter entirely. */
          if (picked.length === CLIENT_ORDER.length) picked = [];
          picked.sort(function (a, b) { return CLIENT_ORDER.indexOf(a) - CLIENT_ORDER.indexOf(b); });
        }
        if (picked.length) root.dataset.clients = picked.join(" ");
        else delete root.dataset.clients;
        try {
          if (picked.length) localStorage.setItem("clients", picked.join(","));
          else localStorage.removeItem("clients");
        } catch (e) { /* private mode */ }
      } else {
        return;
      }
      syncCtx();
    });

    /* A deep link or search landing can target an element inside a block the
       current filter hides; showing everything beats a scroll to nowhere. */
    var revealHashTarget = function () {
      var id = location.hash.slice(1);
      var el = id && document.getElementById(id);
      if (!el || !el.closest(".ctx-variant") || el.offsetParent !== null) return;
      root.dataset.os = "all";
      delete root.dataset.clients;
      syncCtx();
      el.scrollIntoView();
    };
    syncCtx();
    revealHashTarget();
    window.addEventListener("hashchange", revealHashTarget);
  }

  /* ------------------------------------------------------------ configurator */
  /* The setup page's optional command composer. It reads the SAME state the
     context picker writes (data-os / data-clients on <html>), composes the
     canonical one-liner carried in its data attributes with the documented
     env knobs, and never invents a command shape of its own. Hidden without
     JS: the static labeled blocks teach the install on their own. */
  var cfg = document.querySelector(".cfg");
  if (cfg) {
    var cfgOut = cfg.querySelector("[data-cfg-out]");
    var updateCfg = function () {
      var picked = (root.dataset.clients || "").split(" ").filter(Boolean);
      var wantClients = cfg.querySelector('[data-cfg="clients"]').checked && picked.length > 0;
      var noService = cfg.querySelector('[data-cfg="noservice"]').checked;
      var cmd;
      if (root.dataset.os === "windows") {
        var ps = [];
        if (wantClients) ps.push("$env:SEAMLESS_CLIENT='" + picked.join(",") + "'");
        if (noService) ps.push("$env:SEAMLESS_NO_SERVICE='1'");
        cmd = (ps.length ? ps.join("; ") + "; " : "") + cfg.dataset.cmdWin;
      } else {
        var env = [];
        if (wantClients) env.push("SEAMLESS_CLIENT=" + picked.join(","));
        if (noService) env.push("SEAMLESS_NO_SERVICE=1");
        cmd = cfg.dataset.cmdUnix;
        /* Env goes ahead of the shell, not the curl -- the documented form. */
        if (env.length) cmd = cmd.replace("| sh", "| " + env.join(" ") + " sh");
      }
      cfgOut.textContent = cmd;
    };
    cfg.hidden = false;
    cfg.addEventListener("change", updateCfg);
    new MutationObserver(updateCfg).observe(root, { attributes: true, attributeFilter: ["data-os", "data-clients"] });
    updateCfg();
  }

  /* --------------------------------------------------------------- search */
  var input = document.getElementById("search-input");
  var results = document.getElementById("search-results");
  if (!input || !results) return;

  var index = null;
  var loading = false;
  var selected = -1;

  function loadIndex() {
    if (index || loading) return;
    loading = true;
    fetch(docsRoot + "static/search-index.json")
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (docs) {
        index = docs;
        loading = false;
        if (input.value) run();
      })
      .catch(function () {
        /* Search is an enhancement: if the index cannot load, remove the box
           rather than leave an input that silently does nothing. */
        loading = false;
        var box = input.closest(".docs-search");
        if (box) box.remove();
      });
  }

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
      if (!hit) return 0; /* every term must appear somewhere */
      total += hit;
    }
    return total;
  }

  function run() {
    var q = input.value.trim().toLowerCase();
    if (!q || !index) return hide();
    var terms = q.split(/\s+/);
    var hits = [];
    index.forEach(function (doc) {
      var s = score(doc, terms);
      if (s > 0) hits.push({ doc: doc, score: s });
    });
    /* Ties keep nav order: index order is the sidebar's, so equal-scoring pages
       come back in the order the reader already knows. */
    hits.sort(function (a, b) { return b.score - a.score; });
    render(hits.slice(0, 10));
  }

  function render(hits) {
    results.innerHTML = "";
    selected = -1;
    if (!hits.length) {
      results.innerHTML = '<p class="search-empty">No matches.</p>';
    } else {
      hits.forEach(function (h) {
        var a = document.createElement("a");
        a.href = docsRoot + h.doc.url;
        a.setAttribute("role", "option");
        a.innerHTML = '<span class="r-title"></span><span class="r-section"></span>';
        a.querySelector(".r-title").textContent = h.doc.title;
        a.querySelector(".r-section").textContent = h.doc.section;
        results.appendChild(a);
      });
    }
    results.hidden = false;
    input.setAttribute("aria-expanded", "true");
  }

  function hide() {
    results.hidden = true;
    input.setAttribute("aria-expanded", "false");
    selected = -1;
  }

  function move(delta) {
    var links = results.querySelectorAll("a");
    if (!links.length) return;
    if (selected >= 0) links[selected].classList.remove("sel");
    selected = (selected + delta + links.length) % links.length;
    links[selected].classList.add("sel");
    links[selected].scrollIntoView({ block: "nearest" });
  }

  input.addEventListener("focus", loadIndex);
  input.addEventListener("input", run);
  input.addEventListener("keydown", function (ev) {
    if (ev.key === "ArrowDown") { ev.preventDefault(); move(1); }
    else if (ev.key === "ArrowUp") { ev.preventDefault(); move(-1); }
    else if (ev.key === "Enter") {
      var links = results.querySelectorAll("a");
      if (selected >= 0 && links[selected]) { ev.preventDefault(); links[selected].click(); }
    } else if (ev.key === "Escape") { hide(); input.blur(); }
  });
  document.addEventListener("click", function (ev) {
    /* The box removes itself if the index will not load, and this listener
       outlives it -- re-read the ancestor each time rather than assuming one. */
    var box = input.closest(".docs-search");
    if (box && !box.contains(ev.target)) hide();
  });
  /* "/" focuses search, the convention every docs site shares. */
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "/" || ev.metaKey || ev.ctrlKey) return;
    var tag = document.activeElement && document.activeElement.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA") return;
    ev.preventDefault();
    input.focus();
  });
})();
