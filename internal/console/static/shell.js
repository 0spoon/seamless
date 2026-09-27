/* Console shell: the chrome around every page -- the collapsible sidebar, the
   phone drawer, the g-chord shortcuts and the ? sheet, flash banners surfaced
   as toasts, and the recents list the command palette offers. Everything here
   is a per-browser convenience (localStorage), never server state, and every
   piece degrades to the plain server-rendered page without it.

   The shortcut map is read from the sidebar's own links (a[data-key]) at the
   moment a key is pressed, so a feature toggled off in Settings takes its
   shortcut with it and nothing here needs to know the section list. */
(function () {
  'use strict';

  var root = document.documentElement;

  function store(key, value) {
    try {
      if (value === null) localStorage.removeItem(key);
      else localStorage.setItem(key, value);
    } catch (e) {}
  }
  function read(key) {
    try { return localStorage.getItem(key); } catch (e) { return null; }
  }
  function typing(el) {
    if (!el) return false;
    var tag = el.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
  }
  function overlayOpen() {
    var ids = ['cmdk', 'keys'];
    for (var i = 0; i < ids.length; i++) {
      var el = document.getElementById(ids[i]);
      if (el && !el.hidden) return true;
    }
    return false;
  }
  function phone() { return window.matchMedia && window.matchMedia('(max-width: 720px)').matches; }

  /* ---- Sidebar: collapse to an icon rail (desktop) ------------------------ */

  var toggle = document.getElementById('side-toggle');
  function collapsed() { return root.getAttribute('data-sidebar') === 'collapsed'; }
  function syncToggle() {
    if (!toggle) return;
    var c = collapsed();
    toggle.setAttribute('aria-expanded', c ? 'false' : 'true');
    toggle.setAttribute('aria-label', c ? 'Expand sidebar' : 'Collapse sidebar');
    toggle.setAttribute('title', (c ? 'Expand' : 'Collapse') + ' sidebar ([)');
  }
  function setCollapsed(on) {
    if (on) root.setAttribute('data-sidebar', 'collapsed');
    else root.removeAttribute('data-sidebar');
    store('seamless-sidebar', on ? 'collapsed' : null);
    syncToggle();
  }
  if (toggle) toggle.addEventListener('click', function () { setCollapsed(!collapsed()); });
  syncToggle();

  // Collapsed, a section is an icon; its name (and g-chord) appears beside it on
  // hover or focus. Fixed-positioned so the sidebar's own scroll box cannot clip it.
  var tip = null;
  function hideTip() { if (tip) { tip.remove(); tip = null; } }
  function showTip(el) {
    if (!collapsed() || phone()) return;
    var label = el.getAttribute('data-tip');
    if (!label) return;
    hideTip();
    tip = document.createElement('div');
    tip.className = 'side-tip';
    tip.setAttribute('aria-hidden', 'true');
    tip.textContent = label;
    var key = el.getAttribute('data-key');
    if (key) {
      var k = document.createElement('kbd');
      k.textContent = 'g ' + key;
      tip.appendChild(k);
    }
    document.body.appendChild(tip);
    var r = el.getBoundingClientRect();
    tip.style.left = Math.round(r.right + 10) + 'px';
    tip.style.top = Math.round(r.top + r.height / 2) + 'px';
  }
  var side = document.getElementById('sidebar');
  if (side) {
    side.addEventListener('mouseover', function (e) {
      var el = e.target.closest && e.target.closest('[data-tip]');
      if (el && side.contains(el)) showTip(el);
    });
    side.addEventListener('mouseout', function (e) {
      var el = e.target.closest && e.target.closest('[data-tip]');
      if (el && !el.contains(e.relatedTarget)) hideTip();
    });
    side.addEventListener('focusin', function (e) {
      var el = e.target.closest && e.target.closest('[data-tip]');
      if (el) showTip(el);
    });
    side.addEventListener('focusout', hideTip);
    side.addEventListener('scroll', hideTip, { passive: true });
    side.addEventListener('click', hideTip);
  }

  /* ---- Phone drawer -------------------------------------------------------- */

  var sidebar = document.getElementById('sidebar');
  var opener = document.getElementById('drawer-open');
  var closer = document.getElementById('drawer-close');
  var scrim = document.getElementById('drawer-scrim');
  function drawerOpen() { return root.hasAttribute('data-drawer'); }
  function openDrawer() {
    if (!sidebar) return;
    root.setAttribute('data-drawer', 'open');
    if (scrim) scrim.hidden = false;
    if (opener) opener.setAttribute('aria-expanded', 'true');
    var first = sidebar.querySelector('.nav a.active') || sidebar.querySelector('.nav a');
    if (first) { try { first.focus(); } catch (e) {} }
  }
  function closeDrawer(restore) {
    if (!drawerOpen()) return;
    root.removeAttribute('data-drawer');
    if (scrim) scrim.hidden = true;
    if (opener) {
      opener.setAttribute('aria-expanded', 'false');
      if (restore) { try { opener.focus(); } catch (e) {} }
    }
  }
  if (opener) opener.addEventListener('click', openDrawer);
  if (closer) closer.addEventListener('click', function () { closeDrawer(true); });
  if (scrim) scrim.addEventListener('click', function () { closeDrawer(true); });
  if (sidebar) sidebar.addEventListener('click', function (e) {
    // A section link navigates away; the palette trigger opens over the page.
    if (e.target.closest && e.target.closest('.nav a, [data-cmdk-open]')) closeDrawer(false);
  });
  window.addEventListener('resize', function () { if (!phone()) closeDrawer(false); });

  /* ---- Shortcut sheet (?) -------------------------------------------------- */

  var keys = document.getElementById('keys');
  var keysRestore = null;
  function gotoLinks() {
    return Array.prototype.slice.call(document.querySelectorAll('nav.nav a[data-key]'));
  }
  function fillGoto() {
    var dl = document.getElementById('keys-goto');
    if (!dl) return;
    dl.textContent = '';
    gotoLinks().forEach(function (a) {
      var row = document.createElement('div');
      var dt = document.createElement('dt');
      var kbd = document.createElement('kbd');
      kbd.textContent = a.getAttribute('data-key');
      dt.appendChild(kbd);
      var dd = document.createElement('dd');
      var label = a.querySelector('.nav-label');
      dd.textContent = label ? label.textContent : a.getAttribute('data-tip') || a.getAttribute('href');
      row.appendChild(dt);
      row.appendChild(dd);
      dl.appendChild(row);
    });
  }
  function openKeys() {
    if (!keys || !keys.hidden) return;
    fillGoto();
    keysRestore = document.activeElement;
    keys.hidden = false;
    var close = keys.querySelector('[data-keys-close]');
    if (close) close.focus();
  }
  function closeKeys() {
    if (!keys || keys.hidden) return;
    keys.hidden = true;
    if (keysRestore && typeof keysRestore.focus === 'function') { try { keysRestore.focus(); } catch (e) {} }
    keysRestore = null;
  }
  if (keys) keys.addEventListener('click', function (e) {
    if (e.target === keys || (e.target.closest && e.target.closest('[data-keys-close]'))) closeKeys();
  });
  window.SeamShell = { openKeys: openKeys, toggleSidebar: function () { setCollapsed(!collapsed()); } };

  /* ---- g-chords ------------------------------------------------------------ */

  var chordTimer = null;
  var chordHint = null;
  function endChord() {
    if (chordTimer) { clearTimeout(chordTimer); chordTimer = null; }
    if (chordHint) { chordHint.remove(); chordHint = null; }
  }
  function startChord() {
    endChord();
    chordHint = document.createElement('div');
    chordHint.className = 'chord-hint';
    chordHint.setAttribute('role', 'status');
    var lead = document.createElement('kbd');
    lead.textContent = 'g';
    chordHint.appendChild(lead);
    var list = document.createElement('span');
    list.textContent = gotoLinks().map(function (a) {
      var label = a.querySelector('.nav-label');
      return a.getAttribute('data-key') + ' ' + (label ? label.textContent : '');
    }).join('  ·  ');
    chordHint.appendChild(list);
    document.body.appendChild(chordHint);
    chordTimer = setTimeout(endChord, 1600);
  }

  document.addEventListener('keydown', function (e) {
    if (e.defaultPrevented) return;
    if (e.key === 'Escape') {
      if (keys && !keys.hidden) { e.preventDefault(); closeKeys(); return; }
      if (drawerOpen()) { e.preventDefault(); closeDrawer(true); return; }
      endChord();
      return;
    }
    if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target) || overlayOpen()) return;
    if (chordTimer) {
      var key = e.key.toLowerCase();
      var hit = gotoLinks().filter(function (a) { return a.getAttribute('data-key') === key; })[0];
      endChord();
      if (hit) {
        e.preventDefault();
        location.href = hit.getAttribute('href');
      }
      return;
    }
    if (e.key === 'g') { e.preventDefault(); startChord(); return; }
    if (e.key === '?') { e.preventDefault(); openKeys(); return; }
    if (e.key === '[' && !phone()) { e.preventDefault(); setCollapsed(!collapsed()); }
  });

  /* ---- Menus (details.menu) and the page-purpose disclosure ----------------- */

  // A <details> menu stays open across the in-place morph (navigation.js keeps
  // a DETAILS element's open state), so choosing an item must close it here.
  // Outside clicks and Escape close any open menu or (i) disclosure too.
  function closeFloating(except) {
    document.querySelectorAll('details.menu[open], details.page-about[open]').forEach(function (d) {
      if (d !== except) d.open = false;
    });
  }
  document.addEventListener('click', function (e) {
    var inside = e.target.closest ? e.target.closest('details.menu, details.page-about') : null;
    if (inside && e.target.closest('.menu-list a')) { inside.open = false; return; }
    closeFloating(inside);
  });
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Escape') return;
    var open = document.querySelector('details.menu[open], details.page-about[open]');
    if (!open) return;
    e.preventDefault();
    open.open = false;
    var summary = open.querySelector('summary');
    if (summary) summary.focus();
  }, true);

  /* ---- Flash banners as toasts --------------------------------------------- */

  // A mutation redirects back with ?notice= / ?error=; the server renders the
  // banner (the no-JS answer). With script, a success notice becomes a toast and
  // leaves the page, and both params leave the URL, so a later live refresh --
  // which re-fetches location.href -- cannot replay a message about an action
  // that already happened. An error banner stays on the page as well.
  function surfaceFlash() {
    var main = document.querySelector('.main');
    if (!main) return;
    var shown = false;
    main.querySelectorAll('[data-flash]').forEach(function (el) {
      var tone = el.getAttribute('data-flash');
      var text = (el.textContent || '').trim();
      if (text && window.SeamConsole && !shown) {
        window.SeamConsole.flash(text, tone === 'error' ? 'error' : 'ok');
        shown = true;
      }
      if (tone !== 'error') el.remove();
    });
    try {
      var url = new URL(location.href);
      if (url.searchParams.has('notice') || url.searchParams.has('error')) {
        url.searchParams.delete('notice');
        url.searchParams.delete('error');
        history.replaceState(history.state, '', url.pathname + url.search + url.hash);
      }
    } catch (e) {}
  }
  surfaceFlash();
  document.addEventListener('seam:content-updated', surfaceFlash);

  /* ---- Timelines open on "now" --------------------------------------------- */

  // A horizontally scrolling timeline (the capture calendar) reads oldest to
  // newest; on a narrow screen it must open on its newest end, not a year ago.
  // Only the first render is pinned, so a reader's own scrolling is kept.
  // Tracked in a WeakSet, not an attribute: the live morph strips attributes
  // the server did not render, which would re-pin on every refresh.
  var pinned = window.WeakSet ? new WeakSet() : null;
  function pinScrollEnds() {
    document.querySelectorAll('[data-scroll-end]').forEach(function (el) {
      if (pinned && pinned.has(el)) return;
      el.scrollLeft = el.scrollWidth;
      if (pinned) pinned.add(el);
    });
  }
  pinScrollEnds();
  document.addEventListener('seam:content-updated', pinScrollEnds);

  /* ---- Check-in: "since you were last here" -------------------------------- */

  // The stamp is when the owner last stopped LOOKING (a hidden tab or a page
  // they left), so returning to the Overview hours later answers "what changed
  // while I was away?" with the server's own counts. Browsing between pages
  // keeps re-stamping, so a quick round trip never shows a stale digest.
  var SEEN = 'seamless-last-seen';
  var MIN_AWAY = 5 * 60 * 1000;
  var MAX_AWAY = 89 * 24 * 3600 * 1000;
  var answered = null; // the stamp the visible digest answers
  function markSeen() { store(SEEN, String(Date.now())); }
  window.addEventListener('pagehide', markSeen);
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') markSeen();
    else checkin();
  });

  function awayLabel(ms) {
    var m = Math.round(ms / 60000);
    if (m < 60) return m + 'm ago';
    var h = Math.round(m / 60);
    if (h < 48) return h + 'h ago';
    return Math.round(h / 24) + 'd ago';
  }
  function windowFor(ms) {
    var h = ms / 3600000;
    return h <= 24 ? '24h' : h <= 168 ? '7d' : h <= 720 ? '30d' : 'all';
  }
  function chip(n, one, many, href) {
    var tag = document.createElement(href ? 'a' : 'span');
    tag.className = 'checkin-chip';
    if (href) tag.href = href;
    var strong = document.createElement('strong');
    strong.textContent = n;
    tag.appendChild(strong);
    tag.appendChild(document.createTextNode(' ' + (n === 1 ? one : many)));
    return tag;
  }
  function renderCheckin(box, d, away) {
    var body = box.querySelector('.checkin-body');
    if (!body) return;
    body.textContent = '';
    var lead = document.createElement('span');
    lead.className = 'checkin-lead';
    var when = document.createElement('strong');
    when.textContent = awayLabel(away);
    var w = windowFor(away);
    var items = [
      [d.memoriesWritten, 'memory written', 'memories written', '/console/memories?sort=recent'],
      [d.sessions, 'session started', 'sessions started', '/console/sessions?w=' + w],
      [d.tasksClosed, 'task closed', 'tasks closed', '/console/tasks'],
      [d.notesWritten, 'note written', 'notes written', '/console/notes'],
      [d.proposals, 'proposal to review', 'proposals to review', '/console/gardener'],
      [d.mishaps, 'mishap reported', 'mishaps reported', '']
    ].filter(function (it) { return it[0] > 0; });
    if (!items.length) {
      lead.appendChild(document.createTextNode('Quiet since you were last here '));
      lead.appendChild(when);
      lead.appendChild(document.createTextNode(' \u2014 nothing new was recorded.'));
      body.appendChild(lead);
    } else {
      lead.appendChild(document.createTextNode('Since you were last here '));
      lead.appendChild(when);
      body.appendChild(lead);
      items.forEach(function (it) { body.appendChild(chip(it[0], it[1], it[2], it[3])); });
    }
    box.hidden = false;
  }
  function checkin() {
    var box = document.getElementById('checkin');
    if (!box) return;
    var last = parseInt(read(SEEN), 10);
    if (!isFinite(last) || last === answered) return;
    var away = Date.now() - last;
    if (away < MIN_AWAY || away > MAX_AWAY) return;
    answered = last;
    fetch('/console/since?t=' + last, { credentials: 'same-origin', headers: { Accept: 'application/json' } })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) { if (d) renderCheckin(box, d, away); })
      .catch(function () {});
  }
  document.addEventListener('click', function (e) {
    var x = e.target.closest ? e.target.closest('[data-checkin-dismiss]') : null;
    if (!x) return;
    var box = document.getElementById('checkin');
    if (box) box.hidden = true;
  });
  checkin();

  /* ---- Recents for the palette --------------------------------------------- */

  // An entity page, or a rail/reader link to one, is worth offering again. The
  // palette reads this list when it opens with an empty query.
  var ENTITY = /^\/console\/(memories|notes|tasks|plans|sessions|projects|labs|trials|events)\/[^/]+/;
  var KIND = { memories: 'Memory', notes: 'Note', tasks: 'Task', plans: 'Plan', sessions: 'Session', projects: 'Project', labs: 'Lab', trials: 'Trial', events: 'Event' };
  function remember(href, title) {
    var url;
    try { url = new URL(href, location.origin); } catch (e) { return; }
    var m = url.pathname.match(ENTITY);
    if (!m || !title) return;
    title = title.replace(/\s*·\s*Seamless$/, '').trim();
    if (!title) return;
    var list;
    try { list = JSON.parse(read('seamless-recent') || '[]'); } catch (e) { list = []; }
    if (!Array.isArray(list)) list = [];
    list = list.filter(function (r) { return r && r.href !== url.pathname; });
    list.unshift({ href: url.pathname, title: title.slice(0, 120), kind: KIND[m[1]] || '' });
    store('seamless-recent', JSON.stringify(list.slice(0, 8)));
  }
  remember(location.pathname, document.title);
  document.addEventListener('click', function (e) {
    var a = e.target.closest ? e.target.closest('a[href]') : null;
    if (!a || a.closest('.copy-btn')) return;
    var title = a.getAttribute('data-title') || (a.querySelector('.rail-title, strong, .t') || a).textContent;
    remember(a.getAttribute('href'), (title || '').replace(/\s+/g, ' '));
  }, true);
})();
