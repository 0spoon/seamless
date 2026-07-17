/* scenes-player.js -- animates the verbatim with/without terminal transcripts
   from scenes.js into the #scenes section as ONE self-running reel. No
   dependencies, no network, no state beyond the DOM.

   The four scenes share a single viewport slot: a scene-selector nav on top,
   one scene card below. On scroll into view the reel runs a tour and keeps
   looping until the visitor scrolls away:

     play `without` -> hold -> flip to `with` -> hold -> next scene -> ...
     -> wrap back to the first scene

   The split scene (layout:"split") has no without/with; it plays once, holds,
   and advances. Scrolling the reel offscreen pauses the tour; returning resumes
   it. Clicking a scene tab jumps the tour there; clicking the inner without|with
   toggle or replay steers the current scene -- the tour then carries on from
   wherever the visitor left it. prefers-reduced-motion renders transcripts
   statically with no autoplay and no tour. Text is real, selectable DOM.

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

  /* how long to hold on a finished segment before the tour moves on */
  var HOLD_PANE = 2600;   // after `without`, before flipping to `with`
  var HOLD_SCENE = 3800;  // after the last segment, before the next scene

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

  /* animate a pane's body step by step; calls onDone() when finished, and
     nothing at all if superseded (a newer token started). */
  async function playPane(pane, body, st, onDone) {
    var token = ++st.token;
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
    if (onDone) onDone();
  }

  var REPLAY_SVG =
    '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" ' +
    'stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v4h4"/></svg>';

  function sceneHead(scene) {
    var head = document.createElement("div");
    head.className = "ts-head";
    head.innerHTML =
      '<p class="ts-kicker">' + esc(scene.kicker) + "</p>" +
      '<h3 class="ts-title">' + esc(scene.title) + "</h3>" +
      '<p class="ts-ask"><span class="ts-ask-label">prompt</span>' +
      "<span>" + esc(scene.prompt) + "</span></p>";
    return head;
  }

  /* ---- controller: with/without scene (one terminal, a without|with toggle) -
     Returns { el, segments, playSegment, showStatic, stop } for the reel to
     drive. `events` carries the reel's callbacks for user clicks. */
  function buildWithWithout(scene, events, mount) {
    var order = ["without", "with"].filter(function (k) {
      return scene.panes.some(function (p) { return p.key === k; });
    });
    var byKey = {};
    scene.panes.forEach(function (p) { byKey[p.key] = p; });

    mount.appendChild(sceneHead(scene));

    // without|with toggle
    var tabs = document.createElement("div");
    tabs.className = "term-tabs";
    tabs.setAttribute("role", "tablist");
    tabs.setAttribute("aria-label", esc(scene.title) + " -- with or without Seamless");
    var tabEls = {};
    order.forEach(function (key) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "term-tab tab-" + key;
      b.setAttribute("role", "tab");
      b.dataset.pane = key;
      b.innerHTML = '<span class="tab-dot" aria-hidden="true"></span>' + esc(byKey[key].label);
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
    foot.innerHTML = '<p class="ts-outcome"></p>' +
      '<button class="ts-replay" type="button">' + REPLAY_SVG + "replay</button>";
    mount.appendChild(foot);
    var outcomeEl = foot.querySelector(".ts-outcome");

    var state = {};
    order.forEach(function (key) { state[key] = { token: 0 }; });

    function showPane(key) {
      order.forEach(function (k) {
        var on = k === key;
        bodies[k].classList.toggle("on", on);
        tabEls[k].setAttribute("aria-selected", on ? "true" : "false");
        tabEls[k].classList.toggle("active", on);
        tabEls[k].tabIndex = on ? 0 : -1;
      });
      outcomeEl.textContent = byKey[key].outcome;
    }

    function stop() { order.forEach(function (k) { state[k].token++; }); }

    function playSegment(name, onDone) {
      stop();
      showPane(name);
      playPane(byKey[name], bodies[name], state[name], onDone);
    }

    function showStatic(name) {
      name = name || order[0];
      showPane(name);
      staticRender(byKey[name], bodies[name]);
    }

    // initial: idle caret on the first pane
    showPane(order[0]);
    if (!reduced) {
      bodies[order[0]].innerHTML =
        '<div class="ln idle"><span class="p">&gt;</span> <span class="caret"></span></div>';
    }

    order.forEach(function (key) {
      tabEls[key].addEventListener("click", function () {
        if (events.onSegment) events.onSegment(key);
      });
    });
    foot.querySelector(".ts-replay").addEventListener("click", function () {
      if (events.onReplay) events.onReplay();
    });

    return {
      segments: order,
      playSegment: playSegment,
      showStatic: showStatic,
      stop: stop
    };
  }

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

  async function playSplit(groups, bodies, st, onDone) {
    var token = ++st.token;
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
    if (onDone) onDone();
  }

  /* ---- controller: split scene (two agents, one shared timeline) ----------- */
  function buildSplit(scene, events, mount) {
    var order = ["A", "B"]; // display: agent A left, agent B right
    var byKey = {};
    scene.panes.forEach(function (p) { byKey[p.key] = p; });

    mount.appendChild(sceneHead(scene));

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
    var st = { token: 0 };

    function stop() { st.token++; }

    function playSegment(name, onDone) {
      stop();
      playSplit(timeline, bodies, st, onDone);
    }

    function showStatic() {
      order.forEach(function (key) { if (byKey[key]) staticRender(byKey[key], bodies[key]); });
    }

    if (!reduced) {
      order.forEach(function (key) {
        if (byKey[key]) {
          bodies[key].innerHTML =
            '<div class="ln idle"><span class="p">&gt;</span> <span class="caret"></span></div>';
        }
      });
    }

    foot.querySelector(".ts-replay").addEventListener("click", function () {
      if (events.onReplay) events.onReplay();
    });

    return {
      segments: ["single"],
      playSegment: playSegment,
      showStatic: showStatic,
      stop: stop
    };
  }

  /* ---- the reel: one slot, a scene nav, a self-running tour ---------------- */
  function buildReel(reel) {
    var mounts = Array.prototype.slice.call(reel.querySelectorAll(".term-scene[data-scene]"));
    var scenes = mounts.map(function (m) { return byId[m.dataset.scene]; });
    // keep only mounts whose scene exists, preserving order
    var pairs = [];
    mounts.forEach(function (m, i) { if (scenes[i]) pairs.push({ mount: m, scene: scenes[i] }); });
    if (!pairs.length) return;

    var nav = reel.querySelector(".scenes-nav");
    if (!nav) {
      nav = document.createElement("div");
      nav.className = "scenes-nav";
      nav.setAttribute("role", "tablist");
      reel.insertBefore(nav, reel.firstChild);
    }

    var controllers = new Array(pairs.length); // lazy

    // build the scene nav
    var navEls = pairs.map(function (pair, i) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "scene-tab";
      b.setAttribute("role", "tab");
      b.dataset.i = i;
      var num = ("0" + (i + 1)).slice(-2);
      b.innerHTML = '<span class="sr-num">' + num + "</span>" +
        '<span class="sr-label">' + esc(pair.scene.tab || pair.scene.kicker) + "</span>" +
        '<span class="sr-prog" aria-hidden="true"></span>';
      nav.appendChild(b);
      return b;
    });

    function controllerFor(i) {
      if (controllers[i]) return controllers[i];
      var scene = pairs[i].scene;
      var events = {
        onSegment: function (name) { userSegment(i, name); },
        onReplay: function () { userReplay(i); }
      };
      var c = scene.layout === "split"
        ? buildSplit(scene, events, pairs[i].mount)
        : buildWithWithout(scene, events, pairs[i].mount);
      controllers[i] = c;
      return c;
    }

    function showScene(i) {
      pairs.forEach(function (pair, j) {
        pair.mount.classList.toggle("on", j === i);
        navEls[j].classList.toggle("active", j === i);
        navEls[j].setAttribute("aria-selected", j === i ? "true" : "false");
        navEls[j].tabIndex = j === i ? 0 : -1;
        if (j !== i) navEls[j].classList.remove("playing");
      });
    }

    /* ---- tour state --------------------------------------------------------- */
    var cur = 0, seg = 0, holdTimer = null, inView = false, started = false;

    function setProgress(i, frac, ms) {
      var prog = navEls[i].querySelector(".sr-prog");
      if (!prog) return;
      prog.style.transition = "none";
      prog.style.transform = "scaleX(" + (frac ? 1 : 0) + ")";
      if (ms) {
        void prog.offsetWidth; // flush
        prog.style.transition = "transform " + ms + "ms linear";
        prog.style.transform = "scaleX(1)";
      }
    }
    function clearProgress() { navEls.forEach(function (b, i) { setProgress(i, 0, 0); }); }

    function clearHold() {
      if (holdTimer) { clearTimeout(holdTimer); holdTimer = null; }
      clearProgress();
    }

    function holdMs() {
      var segs = controllerFor(cur).segments;
      return seg < segs.length - 1 ? HOLD_PANE : HOLD_SCENE;
    }

    function playSeg() {
      clearHold();
      navEls[cur].classList.add("playing");
      var c = controllerFor(cur);
      c.playSegment(c.segments[seg], onSegDone);
    }

    function onSegDone() {
      if (!inView) return;
      var ms = holdMs();
      setProgress(cur, 0, ms); // telegraph the coming advance
      holdTimer = setTimeout(advance, ms);
    }

    function advance() {
      clearHold();
      var segs = controllerFor(cur).segments;
      if (seg < segs.length - 1) { seg++; playSeg(); return; }
      controllerFor(cur).stop();
      navEls[cur].classList.remove("playing");
      cur = (cur + 1) % pairs.length;
      seg = 0;
      controllerFor(cur); // build before showing to avoid an empty flash
      showScene(cur);
      playSeg();
    }

    /* ---- user steering ------------------------------------------------------ */
    function goScene(i) {
      clearHold();
      if (controllers[cur]) controllers[cur].stop();
      navEls[cur].classList.remove("playing");
      cur = i; seg = 0;
      controllerFor(cur); // build before showing to avoid an empty flash
      showScene(cur);
      playSeg();
    }
    function userSegment(i, name) {
      if (reduced) { pauseSwitch(i); controllerFor(i).showStatic(name); return; }
      clearHold();
      if (controllers[cur] && cur !== i) { controllers[cur].stop(); navEls[cur].classList.remove("playing"); }
      cur = i;
      var c = controllerFor(i);
      var idx = c.segments.indexOf(name);
      seg = idx < 0 ? 0 : idx;
      showScene(i);
      playSeg();
    }
    function userReplay(i) {
      if (reduced) { pauseSwitch(i); controllerFor(i).showStatic(); return; }
      clearHold();
      cur = i;
      showScene(i);
      playSeg();
    }
    function pauseSwitch(i) { cur = i; seg = 0; showScene(i); }

    navEls.forEach(function (b, i) {
      b.addEventListener("click", function () {
        if (reduced) { pauseSwitch(i); controllerFor(i).showStatic(); return; }
        goScene(i);
      });
    });

    /* ---- reduced motion: static, no tour ------------------------------------ */
    if (reduced) {
      showScene(0);
      controllerFor(0).showStatic();
      return;
    }

    // build + show the first scene idle
    controllerFor(0);
    showScene(0);

    // autoplay on scroll into view; leaving pauses, returning resumes
    if ("IntersectionObserver" in window) {
      var io = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          inView = e.isIntersecting;
          if (e.isIntersecting) {
            if (!started) { started = true; playSeg(); }
            else { playSeg(); } // resume the current segment from the top
          } else {
            clearHold();
            if (controllers[cur]) controllers[cur].stop();
            navEls[cur].classList.remove("playing");
          }
        });
      }, { rootMargin: "0px 0px -12% 0px" });
      io.observe(reel);
    } else {
      inView = true; started = true;
      playSeg();
    }
  }

  function init() {
    document.querySelectorAll(".scenes-reel").forEach(buildReel);
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
