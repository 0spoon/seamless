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
    if (!input.closest(".docs-search").contains(ev.target)) hide();
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
