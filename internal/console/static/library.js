/* Library screens (Memories / Notes / Tasks / Plans / Labs / Trials): a grouped rail on
   the left and a document reader on the right. Every rail item is a real server-rendered link;
   this module upgrades clicks to an in-place reader swap (?reader=1 fragment +
   history.pushState of the item's canonical URL) so the rail keeps its scroll
   position and the switch is instant. j / k move the selection. Inert on pages
   without a #lib-reader. */
(function () {
  'use strict';
  var reader = document.getElementById('lib-reader');
  if (!reader) return;
  var base = reader.getAttribute('data-base') || '';

  // Re-queried lazily: the SSE live client can morph the rail in place.
  function items() { return Array.prototype.slice.call(document.querySelectorAll('.lib-rail .rail-item')); }
  function readerEl() { return document.getElementById('lib-reader'); }
  function pathOf(href) {
    try { return new URL(href, location.origin).pathname; } catch (e) { return href; }
  }
  // Scroll the rail's list (never the page) just enough to show the item.
  function revealInRail(a) {
    var disclosure = a.closest ? a.closest('details') : null;
    if (disclosure) disclosure.open = true;
    var rail = a.closest('.rail-scroll') || a.closest('.lib-rail');
    if (!rail) return;
    var r = rail.getBoundingClientRect(), b = a.getBoundingClientRect();
    if (b.top < r.top + 8) rail.scrollTop += b.top - r.top - 8;
    else if (b.bottom > r.bottom - 8) rail.scrollTop += b.bottom - r.bottom + 8;
  }
  function mark(href) {
    var p = pathOf(href);
    var sel = null;
    items().forEach(function (a) {
      var on = pathOf(a.getAttribute('href')) === p;
      a.classList.toggle('selected', on);
      if (on) { a.setAttribute('aria-current', 'page'); sel = a; }
      else { a.removeAttribute('aria-current'); }
    });
    return sel;
  }
  function selectedIndex(list) {
    var idx = -1;
    list.forEach(function (a, i) { if (a.classList.contains('selected')) idx = i; });
    return idx;
  }
  function updateReaderNav() {
    var el = readerEl();
    if (!el) return;
    var nav = el.querySelector('.reader-nav');
    if (!nav) return;
    var list = items(), idx = selectedIndex(list), sel = idx >= 0 ? list[idx] : null;
    var noun = el.getAttribute('aria-label') || 'Item';
    var context = sel && sel.getAttribute('data-context');
    nav.querySelector('.reader-location').textContent = noun + (context ? ' / ' + context : '');
    nav.querySelector('.reader-index').textContent = idx >= 0 ? (idx + 1) + ' of ' + list.length : list.length + ' items';
    var prev = nav.querySelector('[data-reader-step="-1"]');
    var next = nav.querySelector('[data-reader-step="1"]');
    prev.disabled = idx <= 0;
    next.disabled = idx < 0 || idx >= list.length - 1;
  }
  function ensureReaderNav() {
    var el = readerEl();
    if (!el) return;
    var nav = el.querySelector('.reader-nav');
    if (!nav) {
      nav = document.createElement('nav');
      nav.id = 'reader-nav';
      nav.className = 'reader-nav';
      nav.setAttribute('aria-label', 'Reader navigation');
      nav.setAttribute('data-live-skip', '');

      var copy = document.createElement('div');
      copy.className = 'reader-nav-copy';
      var locationLabel = document.createElement('span');
      locationLabel.className = 'reader-location';
      var indexLabel = document.createElement('span');
      indexLabel.className = 'reader-index';
      copy.appendChild(locationLabel);
      copy.appendChild(indexLabel);

      var actions = document.createElement('div');
      actions.className = 'reader-nav-actions';
      var hint = document.createElement('span');
      hint.className = 'reader-keyhint';
      hint.innerHTML = '<kbd>k</kbd> previous <kbd>j</kbd> next';
      var prev = document.createElement('button');
      prev.className = 'reader-step';
      prev.type = 'button';
      prev.textContent = '\u2190';
      prev.title = 'Previous item (k)';
      prev.setAttribute('aria-label', 'Previous item');
      prev.setAttribute('data-reader-step', '-1');
      var next = document.createElement('button');
      next.className = 'reader-step';
      next.type = 'button';
      next.textContent = '\u2192';
      next.title = 'Next item (j)';
      next.setAttribute('aria-label', 'Next item');
      next.setAttribute('data-reader-step', '1');
      actions.appendChild(hint);
      actions.appendChild(prev);
      actions.appendChild(next);
      nav.appendChild(copy);
      nav.appendChild(actions);
      el.insertBefore(nav, el.firstChild);
    }
    updateReaderNav();
  }
  function stepSelection(delta) {
    var list = items();
    if (!list.length) return;
    var idx = selectedIndex(list);
    var next = idx === -1 ? 0 : idx + delta;
    if (next < 0 || next >= list.length) return;
    load(list[next].getAttribute('href'), true);
  }
  function load(href, push) {
    var url = href + (href.indexOf('?') >= 0 ? '&' : '?') + 'reader=1';
    fetch(url, { credentials: 'same-origin' })
      .then(function (r) { if (!r.ok) throw new Error('http ' + r.status); return r.text(); })
      .then(function (html) {
        var el = readerEl();
        if (!el) return;
        el.innerHTML = html;
        if (window.IX && window.IX.enhance) { try { window.IX.enhance(el); } catch (e) {} }
        if (push) { try { history.pushState({ lib: 1 }, '', href); } catch (e) {} }
        var sel = mark(href);
        if (sel) {
          var t = sel.getAttribute('data-title');
          if (t) document.title = t + ' · Seamless';
          revealInRail(sel);
        }
        ensureReaderNav();
        window.scrollTo({ top: 0 });
      })
      .catch(function () { location.href = href; }); // degrade to a plain navigation
  }

  document.addEventListener('click', function (e) {
    var step = e.target.closest ? e.target.closest('[data-reader-step]') : null;
    if (step) {
      e.preventDefault();
      if (!step.disabled) stepSelection(parseInt(step.getAttribute('data-reader-step'), 10));
      return;
    }
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button === 1) return;
    var a = e.target.closest ? e.target.closest('a[href]') : null;
    if (!a) return;
    if (a.closest('.copy-btn')) return;
    var inRail = !!a.closest('.lib-rail') && a.classList.contains('rail-item');
    // Inside the reader, links to siblings of the same entity (e.g. a
    // supersession neighbor) swap in place too; everything else navigates.
    var el = readerEl();
    var inReader = el && el.contains(a) && base && pathOf(a.getAttribute('href')).indexOf(base + '/') === 0;
    if (!inRail && !inReader) return;
    e.preventDefault();
    load(a.getAttribute('href'), true);
  });

  // Browser Back/Forward across swapped selections.
  window.addEventListener('popstate', function () {
    if (base && location.pathname.indexOf(base) === 0) load(location.pathname + location.search, false);
  });

  // j / k step the selection through the rail (list order, across groups),
  // Gmail-style. / focuses the page-local filter where one exists. Arrow keys
  // are left alone so they keep scrolling the document.
  document.addEventListener('keydown', function (e) {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    var ae = document.activeElement;
    if (ae && ae.matches && ae.matches('input, textarea, select, [contenteditable=""], [contenteditable="true"]')) return;
    if (e.key === '/') {
      var query = document.querySelector('.lib-query input[name="q"]');
      if (!query) return;
      e.preventDefault();
      query.focus();
      query.select();
      return;
    }
    if (e.key !== 'j' && e.key !== 'k') return;
    e.preventDefault();
    stepSelection(e.key === 'j' ? 1 : -1);
  });

  // The list URL auto-opens a default selection server-side; pin its canonical
  // URL so a live refresh (which refetches location.href) cannot yank the
  // reader to a different item mid-read.
  var layout = document.querySelector('.lib-layout');
  var auto = layout && layout.getAttribute('data-auto-url');
  if (auto) { try { history.replaceState(null, '', auto); } catch (e) {} }

  // Bring the initial selection into the rail's view.
  var sel0 = items().filter(function (a) { return a.classList.contains('selected'); })[0];
  if (sel0) revealInRail(sel0);
  ensureReaderNav();

  // Live refreshes morph the server-rendered page and can remove this small
  // JS-owned navigation strip. Restore it after any such replacement; the
  // guard makes the observer settle after the single insertion.
  if (window.MutationObserver) {
    var main = document.querySelector('.main');
    if (main) new MutationObserver(function () {
      if (readerEl() && !readerEl().querySelector('.reader-nav')) ensureReaderNav();
    }).observe(main, { childList: true, subtree: true });
  }
})();
