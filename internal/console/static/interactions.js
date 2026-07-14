/* Shared Interactions client module (window.IX).
 *
 * Both Interactions surfaces use it: the live feed (/console/interactions) builds
 * rows from JSON via IX.buildRow, and the project-detail tab upgrades its
 * server-rendered rows via IX.enhance. One value renderer (JSON pretty-print +
 * syntax highlight + escape decode + line clamp), one section builder (with a
 * copy button wired to the global .copy-btn handler), one highlights strip, and
 * one volume-histogram renderer -- so the two surfaces stay visually identical.
 *
 * All dynamic text enters the DOM via textContent (never innerHTML), so
 * highlighting stays XSS-safe. The only innerHTML use is for static, trusted
 * icon path constants below. */
(function (global) {
  'use strict';

  var TEXT_MAX_LINES = 12; // plain-text bodies taller than this start clamped
  var JSON_MAX_LINES = 14; // pretty-JSON bodies taller than this start clamped

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  // ---- icons (a small lucide subset, mirrors internal/console evtIcon) -------
  var ICON = {
    terminal: '<polyline points="4 17 10 11 4 5"/><line x1="12" x2="20" y1="19" y2="19"/>',
    brain: '<path d="M12 5a3 3 0 1 0-5.997.125 4 4 0 0 0-2.526 5.77 4 4 0 0 0 .556 6.588A4 4 0 1 0 12 18Z"/><path d="M12 5a3 3 0 1 1 5.997.125 4 4 0 0 1 2.526 5.77 4 4 0 0 1-.556 6.588A4 4 0 1 1 12 18Z"/><path d="M15 13a4.5 4.5 0 0 1-3-4 4.5 4.5 0 0 1-3 4"/>',
    search: '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>',
    circle: '<circle cx="12" cy="12" r="10"/>',
    'git-fork': '<circle cx="12" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><circle cx="18" cy="6" r="3"/><path d="M18 9v1a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V9"/><path d="M12 12v3"/>',
    map: '<path d="M14.106 5.553a2 2 0 0 0 1.788 0l3.659-1.83A1 1 0 0 1 21 4.619v12.764a1 1 0 0 1-.553.894l-4.553 2.277a2 2 0 0 1-1.788 0l-4.212-2.106a2 2 0 0 0-1.788 0l-3.659 1.83A1 1 0 0 1 3 19.381V6.618a1 1 0 0 1 .553-.894l4.553-2.277a2 2 0 0 1 1.788 0z"/><path d="M15 5.764v15"/><path d="M9 3.236v15"/>',
    activity: '<polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>',
    copy: '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
    check: '<path d="M20 6 9 17l-5-5"/>'
  };

  function svg(name, cls) {
    return '<svg class="ico ' + (cls || '') + '" viewBox="0 0 24 24" fill="none" ' +
      'stroke="currentColor" stroke-width="1.75" stroke-linecap="round" ' +
      'stroke-linejoin="round" aria-hidden="true">' + (ICON[name] || ICON.activity) + '</svg>';
  }

  function iconEl(name, cls) {
    var s = el('span', 'ix-ico' + (cls ? ' ' + cls : ''));
    s.innerHTML = svg(name); // static path constant -- safe
    return s;
  }

  function evtIcon(kind) {
    if (kind === 'tool.call') return 'terminal';
    if (kind === 'retrieval.injected') return 'brain';
    if (kind === 'hook.prompt') return 'search';
    if (kind === 'subagent.captured') return 'git-fork';
    if (kind.indexOf('session.') === 0) return 'circle';
    if (kind.indexOf('plan.') === 0) return 'map';
    return 'activity';
  }

  // ---- value rendering -------------------------------------------------------
  var JSON_TOKEN = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false)\b|\b(null)\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g;

  function tryJSON(s) {
    var c = s.charAt(0);
    if (c !== '{' && c !== '[' && c !== '"') return null;
    try { return { v: JSON.parse(s) }; } catch (e) { return null; }
  }

  // Decode common backslash escapes for a single-line string that still carries
  // literal escape sequences; leave already-decoded text (real newlines/tabs) and
  // genuine backslashes (e.g. paths) untouched.
  function decodeEscapes(s) {
    if (/[\n\r\t]/.test(s)) return s;
    if (!/\\(?:u[0-9a-fA-F]{4}|[nrtbf"\\/])/.test(s)) return s;
    return s.replace(/\\u([0-9a-fA-F]{4})|\\([nrtbf"\\/])/g, function (m, u, c) {
      if (u) return String.fromCharCode(parseInt(u, 16));
      return { n: '\n', r: '\r', t: '\t', b: '\b', f: '\f', '"': '"', '\\': '\\', '/': '/' }[c] || m;
    });
  }

  function unescapeJSONText(tok) {
    return tok.replace(/\\u([0-9a-fA-F]{4})|\\([nrtbf"\\/])/g, function (m, u, c) {
      if (u) return String.fromCharCode(parseInt(u, 16));
      return { n: '\n', r: '', t: '\t', b: '', f: '', '"': '"', '\\': '\\', '/': '/' }[c];
    });
  }

  function jspan(cls, text) { return el('span', cls, text); }

  function highlightInto(pre, text) {
    JSON_TOKEN.lastIndex = 0;
    var last = 0, m;
    while ((m = JSON_TOKEN.exec(text)) !== null) {
      if (m.index > last) pre.appendChild(document.createTextNode(text.slice(last, m.index)));
      if (m[1] !== undefined) {
        if (m[2] !== undefined) {
          pre.appendChild(jspan('jk', unescapeJSONText(m[1])));
          pre.appendChild(document.createTextNode(m[2]));
        } else {
          pre.appendChild(jspan('js', unescapeJSONText(m[1])));
        }
      } else if (m[3] !== undefined) {
        pre.appendChild(jspan('jbool', m[3]));
      } else if (m[4] !== undefined) {
        pre.appendChild(jspan('jnull', m[4]));
      } else {
        pre.appendChild(jspan('jnum', m[5]));
      }
      last = JSON_TOKEN.lastIndex;
    }
    if (last < text.length) pre.appendChild(document.createTextNode(text.slice(last)));
  }

  // addToggle clamps a pre and appends a Show all/Show less button when the body
  // is taller than the threshold.
  function addToggle(wrap, pre, lines) {
    pre.classList.add('clamp');
    var btn = el('button', 'btn small ix-expand', 'Show all ' + lines + ' lines');
    btn.type = 'button';
    btn.onclick = function () {
      var collapsed = pre.classList.toggle('clamp');
      btn.textContent = collapsed ? ('Show all ' + lines + ' lines') : 'Show less';
    };
    wrap.appendChild(btn);
  }

  function countLines(s) {
    var n = 1;
    for (var i = 0; i < s.length; i++) if (s.charCodeAt(i) === 10) n++;
    return n;
  }

  function jsonBlock(value) {
    var pretty;
    try { pretty = JSON.stringify(value, null, 2); } catch (e) { return textBlock(String(value)); }
    var wrap = el('div', 'ix-val');
    var pre = el('pre', 'pre pre-json');
    highlightInto(pre, pretty);
    wrap.appendChild(pre);
    var lines = countLines(unescapeJSONText(pretty));
    if (lines > JSON_MAX_LINES) addToggle(wrap, pre, lines);
    return wrap;
  }

  function textBlock(text) {
    var wrap = el('div', 'ix-val');
    var pre = el('pre', 'pre');
    pre.textContent = text;
    wrap.appendChild(pre);
    var lines = countLines(text);
    if (lines > TEXT_MAX_LINES) addToggle(wrap, pre, lines);
    return wrap;
  }

  // renderValue turns a body string into a highlighted JSON block, decoded text,
  // or a plain clamped text block.
  function renderValue(value) {
    var s = String(value);
    var parsed = tryJSON(s.replace(/^\s+/, ''));
    if (parsed) {
      if (parsed.v !== null && typeof parsed.v === 'object') return jsonBlock(parsed.v);
      if (typeof parsed.v === 'string') return textBlock(parsed.v);
    }
    return textBlock(decodeEscapes(s));
  }

  // ---- section (a titled, copyable request/response panel) -------------------
  function section(title, raw) {
    var sec = el('section', 'ix-section');
    var head = el('div', 'ix-section-head');
    head.appendChild(el('span', 'ix-section-title', title));
    var copy = el('button', 'copy-btn ix-copy');
    copy.type = 'button';
    copy.title = 'Copy';
    copy.setAttribute('aria-label', 'Copy');
    copy.setAttribute('data-copy', String(raw)); // global .copy-btn handler reads this
    copy.innerHTML = svg('copy', 'ico-copy') + svg('check', 'ico-check');
    head.appendChild(copy);
    sec.appendChild(head);
    var body = el('div', 'ix-section-body');
    body.appendChild(renderValue(raw));
    sec.appendChild(body);
    return sec;
  }

  // ---- highlights strip ------------------------------------------------------
  function chip(label, value, tone) {
    var c = el('span', 'ix-chip' + (tone ? ' ' + tone : ''));
    c.appendChild(el('span', 'ix-chip-k', label));
    c.appendChild(el('span', 'ix-chip-v', value));
    return c;
  }

  function highlights(d) {
    var wrap = el('div', 'ix-highlights');
    var n = 0;
    if (d.sessionName || d.sessionId) { wrap.appendChild(chip('session', d.sessionName || shortId(d.sessionId))); n++; }
    if (d.project) { wrap.appendChild(chip('project', d.project)); n++; }
    if (d.ambient) { wrap.appendChild(chip('mode', 'ambient')); n++; }
    if (d.durationMs) { wrap.appendChild(chip('took', d.durationMs + 'ms')); n++; }
    if (d.items) { wrap.appendChild(chip('surfaced', d.items + (d.items === 1 ? ' memory' : ' memories'))); n++; }
    if (d.isError) { wrap.appendChild(chip('status', 'error', 'danger')); n++; }
    return n ? wrap : null;
  }

  // ---- time / id helpers -----------------------------------------------------
  function rel(ts) {
    var t = Date.parse(ts);
    if (isNaN(t)) return '';
    var s = Math.max(0, (Date.now() - t) / 1000);
    if (s < 1) return 'now';
    if (s < 60) return Math.floor(s) + 's';
    if (s < 3600) return Math.floor(s / 60) + 'm';
    if (s < 86400) return Math.floor(s / 3600) + 'h';
    return Math.floor(s / 86400) + 'd';
  }
  function shortId(id) { return id && id.length > 8 ? id.slice(-8) : (id || ''); }

  // ---- buildRow (live feed) --------------------------------------------------
  function buildRow(d) {
    var row = el('details', 'ix-row' + (d.tone ? ' tone-' + d.tone : '') + (d.isError ? ' err' : ''));
    row.setAttribute('data-id', d.id);
    row.setAttribute('data-session', d.sessionId || '');
    row.setAttribute('data-ts', d.ts || '');

    var sum = el('summary');
    sum.appendChild(iconEl(evtIcon(d.kind), 'ix-type'));
    var when = el('span', 'ix-when', rel(d.ts));
    when.title = d.ts;
    sum.appendChild(when);
    sum.appendChild(el('span', 'kind ' + (d.tone || ''), d.kind));
    if (d.label) sum.appendChild(el('span', 'ix-label mono', d.label));
    sum.appendChild(el('span', 'ix-sum', d.summary || ''));
    if (d.ambient) sum.appendChild(el('span', 'badge', 'ambient'));
    if (d.isError) sum.appendChild(el('span', 'badge danger', 'error'));
    var meta = [];
    if (d.durationMs) meta.push(d.durationMs + 'ms');
    if (d.items) meta.push(d.items + (d.items === 1 ? ' item' : ' items'));
    if (meta.length) sum.appendChild(el('span', 'ix-meta', meta.join(' · ')));
    row.appendChild(sum);

    var body = el('div', 'ix-body');
    var hl = highlights(d);
    if (hl) body.appendChild(hl);
    if (d.request) body.appendChild(section(d.kind === 'subagent.captured' ? 'Prompt' : 'Request', d.request));
    if (d.response) {
      var rh = 'Response';
      if (d.kind === 'retrieval.injected') rh = 'Injected content';
      else if (d.kind.indexOf('plan.') === 0) rh = 'Plan content';
      else if (d.kind === 'subagent.captured') rh = 'Report';
      body.appendChild(section(rh, d.response));
    }
    var links = el('div', 'ix-links');
    if (d.sessionId) {
      var sl = el('a', null, (d.sessionName || shortId(d.sessionId)) + ' →');
      sl.href = '/console/sessions/' + d.sessionId;
      sl.setAttribute('data-peek', '');
      links.appendChild(sl);
    }
    var evl = el('a', null, 'event →');
    evl.href = '/console/events/' + d.id;
    evl.setAttribute('data-peek', '');
    links.appendChild(evl);
    body.appendChild(links);
    row.appendChild(body);
    return row;
  }

  // ---- enhance (project-detail server-rendered rows) -------------------------
  // Upgrade each <pre class="ix-raw" data-ix-title="..."> emitted by the server
  // into a formatted, clamped, copyable section.
  function enhance(root) {
    var pres = (root || document).querySelectorAll('pre.ix-raw[data-ix-title]');
    for (var i = 0; i < pres.length; i++) {
      var pre = pres[i];
      var sec = section(pre.getAttribute('data-ix-title'), pre.textContent);
      pre.parentNode.replaceChild(sec, pre);
    }
  }

  // ---- volume histogram ------------------------------------------------------
  var VOL_CATS = ['tool', 'inject', 'prompt', 'session', 'plan'];

  function fmtClock(ts) {
    var t = Date.parse(ts);
    if (isNaN(t)) return '';
    try { return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }); }
    catch (e) { return ''; }
  }

  // renderVolume draws bucketed interaction volume as a stacked bar chart into
  // mount. opts.onSelect(fromMs, toMs, index, barEl) wires click-to-filter; when
  // omitted the bars are hover-only. Returns the bar elements (for selection UI).
  function renderVolume(buckets, mount, opts) {
    opts = opts || {};
    mount.textContent = '';
    if (!buckets || !buckets.length) { mount.classList.add('empty'); return []; }
    var total = 0, maxN = 1;
    for (var j = 0; j < buckets.length; j++) { total += buckets[j].n; if (buckets[j].n > maxN) maxN = buckets[j].n; }
    if (!total) { mount.classList.add('empty'); return []; }
    mount.classList.remove('empty');

    var H = 46;
    var sliceMs = buckets.length > 1 ? (Date.parse(buckets[1].t) - Date.parse(buckets[0].t)) : 60000;
    var track = el('div', 'ix-vol-track');
    var bars = [];
    buckets.forEach(function (b, i) {
      var bar = el('div', 'ix-vbar');
      var barPx = b.n > 0 ? Math.max(2, Math.round(b.n / maxN * H)) : 0;
      bar.style.height = barPx + 'px';
      VOL_CATS.forEach(function (cat) {
        var cnt = b[cat] || 0;
        if (!cnt) return;
        var seg = el('div', 'ix-vseg ' + cat);
        seg.style.height = Math.max(1, Math.round(cnt / b.n * barPx)) + 'px';
        bar.appendChild(seg);
      });
      bar.title = b.n + ' event' + (b.n === 1 ? '' : 's') + (b.t ? ' · ' + fmtClock(b.t) : '');
      if (opts.onSelect && b.n > 0) {
        var from = Date.parse(b.t);
        bar.classList.add('clickable');
        bar.onclick = (function (f, idx, elBar) {
          return function () { opts.onSelect(f, f + sliceMs, idx, elBar); };
        })(from, i, bar);
      }
      track.appendChild(bar);
      bars.push(bar);
    });
    mount.appendChild(track);

    var axis = el('div', 'ix-vol-axis');
    axis.appendChild(el('span', 'ix-vol-t', fmtClock(buckets[0].t)));
    axis.appendChild(el('span', 'ix-vol-total', total + ' interactions'));
    axis.appendChild(el('span', 'ix-vol-t', 'now'));
    mount.appendChild(axis);
    return bars;
  }

  // ---- category filter + volume mounting (server-rendered IX surfaces) --------
  // catOf maps an event kind to its filter/stack category (mirrors the server
  // volCategory and the live feed's local cat), so the .ix-seg segments and the
  // histogram bars agree.
  function catOf(kind) {
    if (kind === 'tool.call') return 'tool';
    if (kind === 'retrieval.injected') return 'inject';
    if (kind === 'hook.prompt') return 'prompt';
    if (kind.indexOf('session.') === 0) return 'session';
    if (kind.indexOf('plan.') === 0 || kind === 'subagent.captured') return 'plan';
    return '';
  }

  // wireKindFilter connects a segmented .ix-seg[data-cat] control to a
  // server-rendered .ix-feed, hiding rows by category (or errors-only). Shared by
  // the project-detail and session-detail surfaces; the live feed runs its own
  // compound filter (session AND category AND time bucket) instead.
  function wireKindFilter(kindsEl, feedEl) {
    if (!kindsEl || !feedEl) return;
    kindsEl.addEventListener('click', function (e) {
      var seg = e.target.closest ? e.target.closest('.ix-seg[data-cat]') : null;
      if (!seg) return;
      var c = seg.getAttribute('data-cat');
      var segs = kindsEl.querySelectorAll('.ix-seg');
      for (var i = 0; i < segs.length; i++) segs[i].classList.toggle('active', segs[i] === seg);
      var rows = feedEl.querySelectorAll('.ix-row');
      for (var j = 0; j < rows.length; j++) {
        var kind = rows[j].getAttribute('data-kind') || '';
        var show = c === 'all' ||
          (c === '__err__' ? rows[j].getAttribute('data-err') === '1' : catOf(kind) === c);
        rows[j].hidden = !show;
      }
    });
  }

  // mountVolume renders the histogram a server embedded as JSON in el's data-vol
  // attribute (static, hover-only). Used by the project- and session-detail pages.
  function mountVolume(el) {
    if (!el) return;
    var buckets;
    try { buckets = JSON.parse(el.getAttribute('data-vol') || '[]'); } catch (e) { return; }
    renderVolume(buckets, el, {});
    el.setAttribute('aria-hidden', buckets.length ? 'false' : 'true');
  }

  global.IX = {
    el: el,
    iconEl: iconEl,
    evtIcon: evtIcon,
    catOf: catOf,
    renderValue: renderValue,
    section: section,
    highlights: highlights,
    rel: rel,
    shortId: shortId,
    buildRow: buildRow,
    enhance: enhance,
    wireKindFilter: wireKindFilter,
    mountVolume: mountVolume,
    renderVolume: renderVolume
  };

  // Auto-upgrade any server-rendered <pre class="ix-raw" data-ix-title> on the
  // page into formatted, copyable IX sections once the DOM is ready, so a plain
  // detail page (event, session timeline, project tab) needs no per-page script
  // to get the feed's value rendering. Fragments injected later (the peek drawer)
  // are enhanced by the drawer loader in layout.html.
  function autoEnhance() { try { enhance(document); } catch (e) {} }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', autoEnhance);
  } else {
    autoEnhance();
  }
})(window);
