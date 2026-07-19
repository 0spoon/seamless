/* Library screens (Notes / Memories / Tasks / Plans): a grouped rail on the left and a
   document reader on the right. Every rail item is a real server-rendered link;
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
        window.scrollTo({ top: 0 });
      })
      .catch(function () { location.href = href; }); // degrade to a plain navigation
  }

  document.addEventListener('click', function (e) {
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
  // Gmail-style. Arrow keys are left alone so they keep scrolling the document.
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'j' && e.key !== 'k') return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    var ae = document.activeElement;
    if (ae && ae.matches && ae.matches('input, textarea, select, [contenteditable=""], [contenteditable="true"]')) return;
    var list = items();
    if (!list.length) return;
    var idx = -1;
    list.forEach(function (a, i) { if (a.classList.contains('selected')) idx = i; });
    var next = idx === -1 ? 0 : (e.key === 'j' ? idx + 1 : idx - 1);
    if (next < 0 || next >= list.length) return;
    e.preventDefault();
    load(list[next].getAttribute('href'), true);
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
})();
