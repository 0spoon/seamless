/* scenes-player.js -- animates the verbatim with/without terminal transcripts
   from scenes.js into the #scenes section. No dependencies, no network, no state
   beyond the DOM.

   Each <div class="term-scene" data-scene="ID"> is filled with a toggled player:
   a dark terminal plus a without|with tab. On scroll into view the active pane
   types itself out, then holds on the punchline and loops (re-typing after a
   beat) for as long as the scene stays in view; scrolling it offscreen pauses
   the loop. A replay button re-runs the active pane; a tab switch plays that
   pane. prefers-reduced-motion renders the full transcript statically with no
   autoplay and no loop. Text is real, selectable DOM.

   Curation lives in the data (scenes.js), never here: this file renders whatever
   steps it is handed, verbatim. Two layouts: `with-without` (one terminal, a
   without|with toggle) and `split` (two terminals on one beat-ordered timeline,
   so the tasks_claim race reads as a race). The files-as-truth epilogue (comment/
   cmd/files/fm steps) is folded into scene 1's with-side ending. */
(function () {
  "use strict";

  var SCENES = window.SEAMLESS_SCENES || [];
  var byId = {};
  SCENES.forEach(function (s) { byId[s.id] = s; });

  var reduced = window.matchMedia &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  var INJECT_LABEL = {
    "seam-briefing": "injected · session start",
    "seam-recall": "injected · prompt recall"
  };

  function wait(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }

  /* ---- markdown-lite for agent prose (trusted, committed data) ------------- */
  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  function inline(s) {
    return esc(s)
      .replace(/`([^`]+)`/g, "<code>$1</code>")
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/\*([^*]+)\*/g, "<em>$1</em>");
  }
  function markdown(text) {
    var out = "";
    var list = null;
    function closeList() { if (list) { out += "</" + list + ">"; list = null; } }
    text.split("\n").forEach(function (line) {
      if (/^\s*$/.test(line)) { closeList(); return; }
      var h = line.match(/^(#{1,4})\s+(.*)$/);
      if (h) { closeList(); out += "<h4>" + inline(h[2]) + "</h4>"; return; }
      if (/^>\s?/.test(line)) { closeList(); out += "<blockquote>" + inline(line.replace(/^>\s?/, "")) + "</blockquote>"; return; }
      var ol = line.match(/^\s*\d+\.\s+(.*)$/);
      if (ol) { if (list !== "ol") { closeList(); out += "<ol>"; list = "ol"; } out += "<li>" + inline(ol[1]) + "</li>"; return; }
      var ul = line.match(/^\s*[-*]\s+(.*)$/);
      if (ul) { if (list !== "ul") { closeList(); out += "<ul>"; list = "ul"; } out += "<li>" + inline(ul[1]) + "</li>"; return; }
      closeList();
      out += "<p>" + inline(line) + "</p>";
    });
    closeList();
    return out;
  }

  /* ---- render one transcript step to an element --------------------------- */
  function renderStep(step) {
    var el = document.createElement("div");
    if (step.role === "user") {
      el.className = "ln user";
      el.innerHTML = '<span class="p">&gt;</span> <span class="typed"></span>';
    } else if (step.role === "inject") {
      el.className = "ln inject";
      var body = esc(step.text);
      (step.focus || []).forEach(function (f) {
        var ef = esc(f);
        body = body.split(ef).join('<mark class="foc">' + ef + "</mark>");
      });
      var label = INJECT_LABEL[step.tag] || ("injected · " + step.tag);
      el.innerHTML = '<span class="inject-tag">' + esc(label) + "</span>" +
        '<span class="inject-body">' + body + "</span>";
    } else if (step.role === "agent") {
      el.className = "ln agent";
      el.innerHTML = markdown(step.text);
    } else if (step.role === "tool") {
      el.className = "ln tool" + (step.emphasis ? " tool-" + step.emphasis : "");
      var res = step.result ? ' <span class="tool-res">' + esc(step.result) + "</span>" : "";
      el.innerHTML = '<span class="tool-dot" aria-hidden="true">●</span> ' +
        '<span class="tool-label">' + esc(step.label) + "</span>" + res;
    } else if (step.role === "ffwd") {
      el.className = "ffwd";
      el.innerHTML = '<span class="ffwd-chip">4×</span> <span>' + esc(step.text) + "</span>";
    } else if (step.role === "comment") {
      el.className = "cl-ln cl-comment";
      el.innerHTML = '<span class="p">$</span> <span class="c">' + esc(step.text) + "</span>";
    } else if (step.role === "cmd") {
      el.className = "cl-ln cl-cmd";
      el.innerHTML = '<span class="p">$</span> ' + esc(step.text);
    } else if (step.role === "files") {
      el.className = "cl-ln cl-files";
      var grid = '<div class="cl-filegrid">';
      (step.files || []).forEach(function (f) {
        var isNew = !!f.tag;
        grid += '<span class="cl-file' + (isNew ? " new" : "") + '">' + esc(f.name) +
          (isNew ? ' <span class="cl-tag">· ' + esc(f.tag) + "</span>" : "") + "</span>";
      });
      el.innerHTML = grid + "</div>";
    } else if (step.role === "fm") {
      el.className = "cl-ln cl-fm";
      el.innerHTML = step.k
        ? '<span class="k">' + esc(step.k) + '</span> <span class="s">' + esc(step.v) + "</span>"
        : '<span class="dim">' + esc(step.v) + "</span>";
    } else {
      el.className = "ln";
      el.textContent = step.text || "";
    }
    return el;
  }

  function delayFor(step) {
    if (reduced) return 0;
    switch (step.role) {
      case "inject": return 1500;
      case "agent": return Math.max(600, Math.min(2600, (step.text || "").length * 12));
      case "tool": return 700;
      case "ffwd": return 950;
      case "user": return 450;
      case "comment": return 520;
      case "cmd": return 520;
      case "files": return 700;
      case "fm": return 240;
      default: return 300;
    }
  }

  function autoscroll(body, el, toFocus) {
    if (reduced) return;
    if (toFocus) {
      var foc = el.querySelector(".foc");
      body.scrollTop = (foc ? foc.offsetTop : el.offsetTop) - 40;
    } else {
      body.scrollTop = body.scrollHeight;
    }
  }

  async function typewriter(span, text, token, st) {
    if (reduced) { span.textContent = text; return; }
    span.parentNode.classList.add("typing");
    var per = Math.min(30, 2200 / Math.max(text.length, 1));
    for (var i = 1; i <= text.length; i++) {
      if (st.token !== token) { span.textContent = text; break; }
      span.textContent = text.slice(0, i);
      await wait(per);
    }
    span.parentNode.classList.remove("typing");
  }

  /* fill a pane's body with its full transcript at once (reduced motion, or
     re-showing a pane already seen) */
  function staticRender(pane, body) {
    body.innerHTML = "";
    pane.steps.forEach(function (step) {
      var el = renderStep(step);
      if (step.role === "user") el.querySelector(".typed").textContent = step.text;
      el.classList.add("show");
      body.appendChild(el);
    });
    body.scrollTop = 0;
  }

  /* animate a pane's body step by step; returns when finished or superseded */
  async function playPane(pane, body, st, onDone) {
    var token = ++st.token;
    st.done = false;
    body.innerHTML = "";
    for (var i = 0; i < pane.steps.length; i++) {
      if (st.token !== token) return;
      var step = pane.steps[i];
      var el = renderStep(step);
      body.appendChild(el);
      if (step.role === "user") {
        await typewriter(el.querySelector(".typed"), step.text, token, st);
        el.classList.add("show");
      } else {
        void el.offsetWidth; // flush so the reveal transition runs
        el.classList.add("show");
        if (step.role === "inject") el.classList.add("pulse");
      }
      autoscroll(body, el, step.role === "inject");
      if (st.token !== token) return;
      await wait(delayFor(step));
    }
    if (st.token !== token) return;
    st.done = true;
    if (onDone) onDone();
  }

  /* ---- build one scene player -------------------------------------------- */
  function buildScene(mount) {
    var scene = byId[mount.dataset.scene];
    if (!scene) return;
    if (scene.layout === "split") { buildSplitScene(mount, scene); return; }
    if (scene.layout !== "with-without") return;

    var panes = scene.panes;
    var order = ["without", "with"];
    var byKey = {};
    panes.forEach(function (p) { byKey[p.key] = p; });

    mount.innerHTML = "";

    // header
    var head = document.createElement("div");
    head.className = "ts-head";
    head.innerHTML =
      '<p class="ts-kicker">' + esc(scene.kicker) + "</p>" +
      '<h3 class="ts-title">' + esc(scene.title) + "</h3>" +
      '<p class="ts-ask"><span class="ts-ask-label">prompt</span>' +
      "<span>" + esc(scene.prompt) + "</span></p>";
    mount.appendChild(head);

    // tabs
    var tabs = document.createElement("div");
    tabs.className = "term-tabs";
    tabs.setAttribute("role", "tablist");
    tabs.setAttribute("aria-label", esc(scene.title) + " -- with or without Seamless");
    var tabEls = {};
    order.forEach(function (key) {
      var p = byKey[key];
      if (!p) return;
      var b = document.createElement("button");
      b.type = "button";
      b.className = "term-tab tab-" + key;
      b.setAttribute("role", "tab");
      b.dataset.pane = key;
      b.innerHTML = '<span class="tab-dot" aria-hidden="true"></span>' + esc(p.label);
      tabs.appendChild(b);
      tabEls[key] = b;
    });
    mount.appendChild(tabs);

    // terminal
    var term = document.createElement("div");
    term.className = "term term-scene-term";
    var bar = document.createElement("div");
    bar.className = "term-bar";
    bar.innerHTML = "<i></i><i></i><i></i><span>~/code/myapp</span>";
    term.appendChild(bar);
    var bodies = {};
    order.forEach(function (key) {
      if (!byKey[key]) return;
      var body = document.createElement("div");
      body.className = "term-body ts-pane";
      body.dataset.pane = key;
      body.setAttribute("role", "tabpanel");
      body.setAttribute("aria-label", esc(byKey[key].label) + " transcript");
      term.appendChild(body);
      bodies[key] = body;
    });
    mount.appendChild(term);

    // footer: outcome + replay
    var foot = document.createElement("div");
    foot.className = "ts-foot";
    foot.innerHTML =
      '<p class="ts-outcome"></p>' +
      '<button class="ts-replay" type="button">' +
      '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" ' +
      'stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v4h4"/></svg>replay</button>';
    mount.appendChild(foot);
    var outcomeEl = foot.querySelector(".ts-outcome");
    var replayBtn = foot.querySelector(".ts-replay");

    // per-pane playback state
    var state = {};
    order.forEach(function (key) { if (byKey[key]) state[key] = { token: 0, done: false }; });
    var active = order[0];

    // loop: after a pane finishes, hold on the punchline then re-type, but only
    // while the scene is in view (offscreen scenes pause instead of spinning).
    var LOOP_HOLD = 3800;
    var loopTimer = null;
    var inView = false;
    var started = false;
    function clearLoop() { if (loopTimer) { clearTimeout(loopTimer); loopTimer = null; } }
    function scheduleLoop(key) {
      clearLoop();
      loopTimer = setTimeout(function () {
        if (reduced || active !== key || !inView) return;
        activate(key, true);
      }, LOOP_HOLD);
    }

    function showPane(key) {
      active = key;
      order.forEach(function (k) {
        if (!byKey[k]) return;
        var on = k === key;
        bodies[k].classList.toggle("on", on);
        tabEls[k].setAttribute("aria-selected", on ? "true" : "false");
        tabEls[k].classList.toggle("active", on);
        tabEls[k].tabIndex = on ? 0 : -1;
      });
      outcomeEl.textContent = byKey[key].outcome;
    }

    function hintOther(fromKey) {
      order.forEach(function (k) {
        if (byKey[k]) tabEls[k].classList.toggle("hint", k !== fromKey && !state[k].done);
      });
    }
    function clearHints() { order.forEach(function (k) { if (byKey[k]) tabEls[k].classList.remove("hint"); }); }

    function activate(key, forceReplay) {
      clearLoop();
      // stop any other pane mid-play
      order.forEach(function (k) { if (k !== key && state[k]) state[k].token++; });
      showPane(key);
      var st = state[key];
      var pane = byKey[key];
      if (reduced) {
        staticRender(pane, bodies[key]);
        return;
      }
      if (forceReplay || !st.done) {
        playPane(pane, bodies[key], st, function () { hintOther(key); scheduleLoop(key); });
      } else {
        staticRender(pane, bodies[key]); // already seen -> show final at once, then loop
        scheduleLoop(key);
      }
    }

    order.forEach(function (key) {
      if (!byKey[key]) return;
      tabEls[key].addEventListener("click", function () {
        clearHints();
        if (active === key && !state[key].done) return; // already playing this one
        activate(key, false);
      });
    });
    replayBtn.addEventListener("click", function () {
      clearHints();
      activate(active, true);
    });

    // initial state
    showPane(order[0]);
    if (reduced) {
      staticRender(byKey[order[0]], bodies[order[0]]);
      // both panes reachable via tabs; static-render lazily on switch
      return;
    }
    bodies[order[0]].innerHTML = '<div class="ln idle"><span class="p">&gt;</span> <span class="caret"></span></div>';

    // autoplay on scroll into view; keep observing so leaving the viewport pauses
    // the loop and returning resumes it.
    if ("IntersectionObserver" in window) {
      var io = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          inView = e.isIntersecting;
          if (e.isIntersecting) {
            if (!started) { started = true; activate(order[0], false); }
            else if (state[active] && state[active].done) { scheduleLoop(active); }
          } else {
            clearLoop();
          }
        });
      }, { rootMargin: "0px 0px -12% 0px" });
      io.observe(mount);
    } else {
      inView = true;
      activate(order[0], false);
    }
  }

  /* ---- scene 3: two agents on ONE shared timeline (layout:"split") --------- */
  var REPLAY_SVG =
    '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" ' +
    'stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v4h4"/></svg>';

  /* merge both panes' steps into one beat-ordered sequence, grouped so that
     steps sharing a beat fire together and the claim collision reads as a race:
     B's winning claim on its beat, A's bounce on the next. */
  function buildTimeline(panes) {
    var items = [];
    panes.forEach(function (p, pi) {
      p.steps.forEach(function (step, si) {
        items.push({
          pane: p.key, paneIndex: pi, step: step,
          beat: typeof step.beat === "number" ? step.beat : si
        });
      });
    });
    items.sort(function (a, b) {
      return a.beat !== b.beat ? a.beat - b.beat : a.paneIndex - b.paneIndex;
    });
    var groups = [];
    items.forEach(function (it) {
      var last = groups[groups.length - 1];
      if (last && last.beat === it.beat) last.items.push(it);
      else groups.push({ beat: it.beat, items: [it] });
    });
    return groups;
  }

  async function revealSplitStep(step, body, token, st) {
    var el = renderStep(step);
    body.appendChild(el);
    if (step.role === "user") {
      await typewriter(el.querySelector(".typed"), step.text, token, st);
      el.classList.add("show");
    } else {
      void el.offsetWidth; // flush so the reveal transition runs
      el.classList.add("show");
      if (step.role === "inject") el.classList.add("pulse");
    }
    autoscroll(body, el, step.role === "inject");
  }

  async function playSplit(groups, bodies, st) {
    var token = ++st.token;
    st.done = false;
    Object.keys(bodies).forEach(function (k) { bodies[k].innerHTML = ""; });
    for (var gi = 0; gi < groups.length; gi++) {
      if (st.token !== token) return;
      var group = groups[gi];
      await Promise.all(group.items.map(function (it) {
        return revealSplitStep(it.step, bodies[it.pane], token, st);
      }));
      if (st.token !== token) return;
      var gap = 0;
      group.items.forEach(function (it) { gap = Math.max(gap, delayFor(it.step)); });
      await wait(gap);
    }
    if (st.token !== token) return;
    st.done = true;
  }

  function buildSplitScene(mount, scene) {
    var order = ["A", "B"]; // display: agent A left, agent B right
    var byKey = {};
    scene.panes.forEach(function (p) { byKey[p.key] = p; });

    mount.innerHTML = "";

    var head = document.createElement("div");
    head.className = "ts-head";
    head.innerHTML =
      '<p class="ts-kicker">' + esc(scene.kicker) + "</p>" +
      '<h3 class="ts-title">' + esc(scene.title) + "</h3>" +
      '<p class="ts-ask"><span class="ts-ask-label">prompt</span>' +
      "<span>" + esc(scene.prompt) + "</span></p>";
    mount.appendChild(head);

    var split = document.createElement("div");
    split.className = "ts-split";
    var bodies = {};
    order.forEach(function (key) {
      var p = byKey[key];
      if (!p) return;
      var col = document.createElement("div");
      col.className = "ts-col";
      var term = document.createElement("div");
      term.className = "term term-scene-term ts-split-term";
      var bar = document.createElement("div");
      bar.className = "term-bar";
      bar.innerHTML = "<i></i><i></i><i></i><span>" + esc(p.label) + " · ~/code/myapp</span>";
      term.appendChild(bar);
      var body = document.createElement("div");
      body.className = "term-body ts-splitpane";
      body.dataset.pane = key;
      body.setAttribute("role", "group");
      body.setAttribute("aria-label", esc(p.label) + " transcript");
      term.appendChild(body);
      col.appendChild(term);
      var outcome = document.createElement("p");
      outcome.className = "ts-outcome ts-col-outcome";
      outcome.textContent = p.outcome;
      col.appendChild(outcome);
      split.appendChild(col);
      bodies[key] = body;
    });
    mount.appendChild(split);

    var foot = document.createElement("div");
    foot.className = "ts-foot ts-foot-split";
    foot.innerHTML = '<button class="ts-replay" type="button">' + REPLAY_SVG + "replay</button>";
    mount.appendChild(foot);

    var timeline = buildTimeline(scene.panes);
    var st = { token: 0, done: false };

    function renderStatic() {
      order.forEach(function (key) { if (byKey[key]) staticRender(byKey[key], bodies[key]); });
    }

    // loop the shared timeline while in view; pause offscreen (see buildScene).
    var LOOP_HOLD = 4200;
    var loopTimer = null;
    var inView = false;
    var started = false;
    function clearLoop() { if (loopTimer) { clearTimeout(loopTimer); loopTimer = null; } }
    function scheduleLoop() {
      clearLoop();
      loopTimer = setTimeout(function () { if (!reduced && inView) runSplit(); }, LOOP_HOLD);
    }
    function runSplit() {
      clearLoop();
      playSplit(timeline, bodies, st).then(function () {
        if (st.done && !reduced && inView) scheduleLoop();
      });
    }

    foot.querySelector(".ts-replay").addEventListener("click", function () {
      if (reduced) renderStatic();
      else runSplit();
    });

    if (reduced) { renderStatic(); return; }

    order.forEach(function (key) {
      if (byKey[key]) {
        bodies[key].innerHTML =
          '<div class="ln idle"><span class="p">&gt;</span> <span class="caret"></span></div>';
      }
    });

    if ("IntersectionObserver" in window) {
      var io = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          inView = e.isIntersecting;
          if (e.isIntersecting) {
            if (!started) { started = true; runSplit(); }
            else if (st.done) { scheduleLoop(); }
          } else {
            clearLoop();
          }
        });
      }, { rootMargin: "0px 0px -12% 0px" });
      io.observe(mount);
    } else {
      inView = true;
      runSplit();
    }
  }

  function init() {
    document.querySelectorAll(".term-scene[data-scene]").forEach(buildScene);
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
