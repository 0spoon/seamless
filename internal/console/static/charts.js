/* Seamless console -- the interaction layer for the server-rendered SVG charts
   in charts.go. It adds exactly one thing: a hover/focus readout of the value at
   a given point in time, on any .area[data-hover] chart (the injection trend and
   the coverage trend). Deliberately not a chart library: every number, label and
   unit it shows was formatted server-side and handed over in the data-hover
   payload, so this file only picks the point nearest the pointer and places the
   crosshair and tooltip.

   The layer is rebuilt on demand from the payload rather than mounted once at
   load. The SSE live client morphs .main in place and strips any node absent
   from the freshly-fetched copy -- which is every node this file adds. Rebuilding
   on the next pointer/focus event survives that, and picks up the refreshed
   numbers for free. */
(function () {
  var NS = 'http://www.w3.org/2000/svg';
  var cache = new WeakMap(); // .area -> layer, valid while its payload is unchanged
  var active = null;         // the .area currently showing a readout

  function svgEl(name, attrs) {
    var e = document.createElementNS(NS, name);
    for (var k in attrs) e.setAttribute(k, String(attrs[k]));
    return e;
  }

  function div(cls, text) {
    var e = document.createElement('div');
    e.className = cls;
    if (text) e.textContent = text;
    return e;
  }

  function build(area, raw) {
    var data;
    try { data = JSON.parse(raw); } catch (e) { return null; }
    if (!data || !data.pts || !data.pts.length) return null;
    var svg = area.querySelector('svg.area-svg');
    if (!svg) return null;

    var rule = svgEl('line', { class: 'hov-rule', y1: data.top, y2: data.bot, 'vector-effect': 'non-scaling-stroke' });
    var dot = svgEl('circle', { class: 'hov-dot', r: 4.5, 'vector-effect': 'non-scaling-stroke' });
    var g = svgEl('g', { class: 'hov', 'aria-hidden': 'true' });
    g.appendChild(rule);
    g.appendChild(dot);
    // A transparent capture rect over the plot, added last so it sits on top: it
    // makes the gaps between points hoverable, and it masks the <title> fallbacks
    // on the coverage dots so the browser's own tooltip can't double up with ours.
    var hit = svgEl('rect', { class: 'hov-hit', x: 0, y: 0, width: data.w, height: data.h });
    svg.appendChild(g);
    svg.appendChild(hit);

    var tip = div('hov-tip');
    tip.hidden = true;
    area.appendChild(tip);

    var layer = { raw: raw, data: data, svg: svg, rule: rule, dot: dot, tip: tip, i: -1 };
    cache.set(area, layer);
    return layer;
  }

  function layerOf(area) {
    var raw = area.getAttribute('data-hover') || '';
    var l = cache.get(area);
    // isConnected catches the live morph having stripped the layer out from under us.
    if (l && l.raw === raw && l.tip.isConnected && l.svg.isConnected) return l;
    return build(area, raw);
  }

  // px maps viewBox units to page pixels. The svg carries a viewBox and is sized
  // width:100%/height:auto in CSS, so it scales uniformly -- one factor covers both axes.
  function px(l) {
    var r = l.svg.getBoundingClientRect();
    return { k: r.width / l.data.w || 1, left: r.left, top: r.top };
  }

  function nearest(l, clientX) {
    var s = px(l);
    var vx = (clientX - s.left) / s.k;
    var pts = l.data.pts, best = 0, bd = Infinity;
    for (var i = 0; i < pts.length; i++) {
      var d = Math.abs(pts[i].x - vx);
      if (d < bd) { bd = d; best = i; }
    }
    return best;
  }

  function place(area, l, p) {
    var s = px(l), ar = area.getBoundingClientRect();
    var cx = s.left - ar.left + p.x * s.k;
    var cy = s.top - ar.top + p.y * s.k;
    var w = l.tip.offsetWidth, h = l.tip.offsetHeight;
    // Centre on the point, then clamp inside the card; flip below when the point
    // sits too near the top for the tooltip to clear it.
    var left = Math.max(0, Math.min(cx - w / 2, area.clientWidth - w));
    var top = cy - h - 12;
    if (top < 0) top = cy + 14;
    l.tip.style.left = Math.round(left) + 'px';
    l.tip.style.top = Math.round(top) + 'px';
  }

  function show(area, l, i) {
    var p = l.data.pts[i];
    if (!p) return;
    if (l.i === i && !l.tip.hidden) return; // nothing moved -- don't re-measure
    l.rule.setAttribute('x1', p.x);
    l.rule.setAttribute('x2', p.x);
    l.dot.setAttribute('cx', p.x);
    l.dot.setAttribute('cy', p.y);
    l.tip.textContent = '';
    l.tip.appendChild(div('hov-t', p.label));
    l.tip.appendChild(div('hov-v', p.value));
    if (p.sub) l.tip.appendChild(div('hov-s', p.sub));
    l.tip.hidden = false;
    area.classList.add('hov-on');
    place(area, l, p); // after unhiding: the clamp needs the tooltip's real box
    l.i = i;
    active = area;
  }

  function hide(area) {
    var l = cache.get(area);
    if (l) { l.tip.hidden = true; l.i = -1; }
    area.classList.remove('hov-on');
    if (active === area) active = null;
  }

  function areaOf(t) { return t && t.closest ? t.closest('.area[data-hover]') : null; }

  // Delegated from the document: the charts are server-rendered and the live
  // client can replace them wholesale, so nothing may hold a node reference.
  function track(e) {
    var area = areaOf(e.target);
    if (!area) { if (active) hide(active); return; }
    if (active && active !== area) hide(active);
    var l = layerOf(area);
    if (l) show(area, l, nearest(l, e.clientX));
  }
  document.addEventListener('pointermove', track, { passive: true });
  document.addEventListener('pointerdown', track, { passive: true }); // touch: tap to read
  document.addEventListener('pointerleave', function () { if (active) hide(active); }, { passive: true });

  // Keyboard: the chart svg is focusable (tabindex is server-rendered), arrows
  // walk the series. Screen readers get the svg's aria-label summary instead --
  // the readout is a sighted-keyboard affordance, so it stays aria-hidden.
  document.addEventListener('focusin', function (e) {
    var area = areaOf(e.target);
    if (!area) { if (active) hide(active); return; }
    var l = layerOf(area);
    if (l) show(area, l, l.data.pts.length - 1); // land on the most recent bucket
  });
  document.addEventListener('focusout', function (e) {
    var area = areaOf(e.target);
    if (area) hide(area);
  });
  document.addEventListener('keydown', function (e) {
    var area = areaOf(e.target);
    if (!area) return;
    var l = layerOf(area);
    if (!l) return;
    var n = l.data.pts.length;
    var i = l.i < 0 ? n - 1 : l.i;
    var next;
    switch (e.key) {
      case 'ArrowLeft': next = Math.max(0, i - 1); break;
      case 'ArrowRight': next = Math.min(n - 1, i + 1); break;
      case 'Home': next = 0; break;
      case 'End': next = n - 1; break;
      case 'Escape': hide(area); return;
      default: return;
    }
    e.preventDefault();
    show(area, l, next);
  });
})();

/* Capture calendar (the Sessions page). The same contract as the trend readout
   above -- every string shown was formatted server-side -- but the cells are
   discrete rects, so the readout keys off each cell's own data-day and <title>
   rather than a nearest-point search. Three affordances:

   - Hover (or the keyboard cursor) shows the day's numbers in the shared
     .hov-tip readout, instantly, in place of the browser's sluggish native
     tooltip: enhance() lifts each <title> into a data-t attribute so the
     native one cannot double up. The morph restores <title>s from the fresh
     server copy; re-enhancing on seam:content-updated lifts them again.
   - Click (or Enter on the keyboard cursor) focuses the session map on that
     day via the shared no-reload path: it mints the ?day URL the server
     defined, preserving the other filters; clicking the already-focused day
     clears it. Real navigation stays the no-JS fallback via the svg's titles.
   - Arrow keys walk days from today (the svg is server-rendered focusable):
     up/down one day, left/right one week, Home/End the span's edges. */
(function () {
  function grids() { return document.querySelectorAll('.mom-cal-grid'); }
  function cellsOf(grid) { return grid.querySelectorAll('.mom-cal-cell'); }

  function enhance() {
    grids().forEach(function (grid) {
      cellsOf(grid).forEach(function (cell) {
        var title = cell.querySelector('title');
        if (!title) return;
        cell.setAttribute('data-t', title.textContent);
        cell.removeChild(title);
      });
    });
  }
  enhance();
  document.addEventListener('seam:content-updated', enhance);

  function calOf(t) { return t && t.closest ? t.closest('.mom-cal') : null; }
  function cellOf(t) { return t && t.closest ? t.closest('.mom-cal-cell') : null; }

  function div(cls, text) {
    var e = document.createElement('div');
    e.className = cls;
    if (text) e.textContent = text;
    return e;
  }

  function tipOf(cal) {
    var tip = cal.querySelector(':scope > .hov-tip');
    if (!tip) {
      tip = div('hov-tip');
      tip.hidden = true;
      cal.appendChild(tip);
    }
    return tip;
  }

  function show(cell) {
    var cal = calOf(cell);
    if (!cal) return;
    var raw = cell.getAttribute('data-t') || '';
    var cut = raw.indexOf(' -- ');
    var tip = tipOf(cal);
    tip.textContent = '';
    tip.appendChild(div('hov-t', cut < 0 ? raw : raw.slice(0, cut)));
    tip.appendChild(div('hov-v', cut < 0 ? '' : raw.slice(cut + 4)));
    tip.appendChild(div('hov-s', cell.classList.contains('sel')
      ? 'Click to clear the day focus'
      : 'Click to focus the session map on this day'));
    tip.hidden = false;
    // Above the cell, centred and clamped inside the card; below when the top
    // row would clip it. Rect math, so the horizontal scroll is accounted for.
    var cr = cell.getBoundingClientRect(), ar = cal.getBoundingClientRect();
    var cx = cr.left - ar.left + cr.width / 2;
    var left = Math.max(0, Math.min(cx - tip.offsetWidth / 2, cal.clientWidth - tip.offsetWidth));
    var top = cr.top - ar.top - tip.offsetHeight - 10;
    if (top < 0) top = cr.bottom - ar.top + 10;
    tip.style.left = Math.round(left) + 'px';
    tip.style.top = Math.round(top) + 'px';
  }

  function hide(cal) {
    var tip = cal && cal.querySelector(':scope > .hov-tip');
    if (tip) tip.hidden = true;
    var kb = cal && cal.querySelector('.mom-cal-cell.kb');
    if (kb) kb.classList.remove('kb');
  }

  function open(cell) {
    var url = new URL(location.href);
    if (cell.classList.contains('sel')) url.searchParams.delete('day');
    else url.searchParams.set('day', cell.getAttribute('data-day') || '');
    if (window.SeamConsole && window.SeamConsole.load) {
      window.SeamConsole.load(url.href, { history: 'push', source: 'query' });
    } else {
      location.assign(url.href);
    }
  }

  document.addEventListener('pointerover', function (e) {
    var cell = cellOf(e.target);
    if (cell) { enhanceCell(cell); show(cell); }
    else hide(calOf(e.target));
  }, { passive: true });
  document.addEventListener('pointerout', function (e) {
    var cal = calOf(e.target);
    if (cal && !calOf(e.relatedTarget)) hide(cal);
  }, { passive: true });
  document.addEventListener('click', function (e) {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    var cell = cellOf(e.target);
    if (cell) open(cell);
  });

  // A cell hovered in the sliver between the morph restoring <title>s and the
  // enhance pass: lift lazily so the readout never shows stale emptiness.
  function enhanceCell(cell) {
    var title = cell.querySelector('title');
    if (!title) return;
    cell.setAttribute('data-t', title.textContent);
    cell.removeChild(title);
  }

  // Keyboard: the grid is one tab stop; arrows move a .kb cursor cell starting
  // from today (the last cell), Enter or Space opens it, Escape rests.
  function cursor(grid, cells, next) {
    var cur = grid.querySelector('.mom-cal-cell.kb');
    if (cur) cur.classList.remove('kb');
    var cell = cells[next];
    if (!cell) return;
    cell.classList.add('kb');
    enhanceCell(cell);
    show(cell);
  }
  document.addEventListener('focusin', function (e) {
    var t = e.target;
    if (t && t.classList && t.classList.contains('mom-cal-grid')) {
      var cells = cellsOf(t);
      cursor(t, cells, cells.length - 1);
    }
  });
  document.addEventListener('focusout', function (e) {
    var t = e.target;
    if (t && t.classList && t.classList.contains('mom-cal-grid')) hide(calOf(t));
  });
  document.addEventListener('keydown', function (e) {
    var t = e.target;
    if (!t || !t.classList || !t.classList.contains('mom-cal-grid')) return;
    var cells = cellsOf(t);
    var cur = t.querySelector('.mom-cal-cell.kb');
    var i = cur ? Array.prototype.indexOf.call(cells, cur) : cells.length - 1;
    var n = cells.length;
    var next;
    switch (e.key) {
      case 'ArrowUp': next = Math.max(0, i - 1); break;
      case 'ArrowDown': next = Math.min(n - 1, i + 1); break;
      case 'ArrowLeft': next = Math.max(0, i - 7); break;
      case 'ArrowRight': next = Math.min(n - 1, i + 7); break;
      case 'Home': next = 0; break;
      case 'End': next = n - 1; break;
      case 'Enter': case ' ':
        if (cur) { e.preventDefault(); open(cur); }
        return;
      case 'Escape': hide(calOf(t)); return;
      default: return;
    }
    e.preventDefault();
    cursor(t, cells, next);
  });
})();
