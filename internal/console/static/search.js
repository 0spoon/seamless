/* Command palette: Cmd/Ctrl-K (or "/" outside an input) opens a search overlay
   on every console page. It fetches the same GET /console/search route the page
   uses, via its ?format=json short-circuit, with fast=1 so a query per keystroke
   never triggers a remote embedding call (see console/search.go).

   Inert when #cmdk is absent (the login page has no palette). */
(function () {
  var overlay = document.getElementById('cmdk');
  if (!overlay) return;

  var panel = overlay.querySelector('.cmdk');
  var input = document.getElementById('cmdk-input');
  var list = document.getElementById('cmdk-list');
  var allLink = document.getElementById('cmdk-all');

  var MIN_CHARS = 2;      // matches the server's floor (store's ftsQuery drops 1-char tokens)
  var DEBOUNCE_MS = 200;
  var PER_GROUP = 5;

  var rows = [];          // flat list of {href} in render order, for arrow keys
  var sel = -1;
  var timer = null;
  var inflight = null;    // AbortController for the outstanding fetch
  var restoreFocus = null;

  function isOpen() { return !overlay.hidden; }

  function searchHref(q) {
    return '/console/search?q=' + encodeURIComponent(q || '');
  }

  function open() {
    if (isOpen()) return;
    restoreFocus = document.activeElement;
    overlay.hidden = false;
    input.value = '';
    render(null);
    input.focus();
  }

  function close() {
    if (!isOpen()) return;
    if (inflight) { inflight.abort(); inflight = null; }
    if (timer) { clearTimeout(timer); timer = null; }
    overlay.hidden = true;
    panel.classList.remove('loading');
    list.innerHTML = '';
    rows = [];
    sel = -1;
    if (restoreFocus && typeof restoreFocus.focus === 'function') {
      try { restoreFocus.focus(); } catch (e) {}
    }
    restoreFocus = null;
  }

  function setSelected(i) {
    var opts = list.querySelectorAll('.cmdk-opt');
    if (!opts.length) { sel = -1; input.removeAttribute('aria-activedescendant'); return; }
    if (i < 0) i = opts.length - 1;
    if (i >= opts.length) i = 0;
    sel = i;
    opts.forEach(function (o, n) {
      var on = n === i;
      o.classList.toggle('selected', on);
      o.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    var cur = opts[i];
    input.setAttribute('aria-activedescendant', cur.id);
    if (cur.scrollIntoView) cur.scrollIntoView({ block: 'nearest' });
  }

  function msg(cls, text) {
    var d = document.createElement('div');
    d.className = cls;
    d.textContent = text;
    list.innerHTML = '';
    list.appendChild(d);
  }

  /* render paints the groups. Every field goes in via textContent EXCEPT
     snippetHtml, which is the ONE innerHTML in this file: it is server-generated
     HTML from console/search.go's highlightSnippet, which escapes the item text
     before substituting <mark>. Nothing else here may become innerHTML. */
  function render(data) {
    rows = [];
    sel = -1;
    input.removeAttribute('aria-activedescendant');
    if (!data) { msg('cmdk-empty', 'Type at least ' + MIN_CHARS + ' characters to search.'); return; }
    if (!data.groups || !data.groups.length) { msg('cmdk-empty', 'No results.'); return; }

    list.innerHTML = '';
    data.groups.forEach(function (g) {
      var head = document.createElement('li');
      head.className = 'cmdk-group';
      head.setAttribute('role', 'presentation');
      head.textContent = g.label + ' ';
      var n = document.createElement('span');
      n.className = 'n';
      n.textContent = g.count;
      head.appendChild(n);
      list.appendChild(head);

      (g.rows || []).slice(0, PER_GROUP).forEach(function (r) {
        var li = document.createElement('li');
        li.className = 'cmdk-opt';
        li.id = 'cmdk-opt-' + rows.length;
        li.setAttribute('role', 'option');
        li.setAttribute('aria-selected', 'false');

        var t = document.createElement('span');
        t.className = 't';
        t.textContent = r.title;
        li.appendChild(t);

        var d = document.createElement('span');
        d.className = 'd';
        if (r.snippetHtml) {
          d.innerHTML = r.snippetHtml; // server-escaped; see the note above
        } else {
          d.textContent = r.description || '';
        }
        li.appendChild(d);

        var meta = document.createElement('span');
        meta.className = 'meta';
        meta.textContent = (r.project || 'global') + ' · ' + r.age;
        li.appendChild(meta);

        var idx = rows.length;
        li.addEventListener('mousemove', function () { setSelected(idx); });
        li.addEventListener('click', function (e) { go(r.href, e.metaKey || e.ctrlKey); });
        list.appendChild(li);
        rows.push({ href: r.href });
      });
    });
    if (rows.length) setSelected(0);
  }

  function go(href, newTab) {
    if (!href) return;
    if (newTab) { window.open(href, '_blank'); return; }
    close();
    location.href = href;
  }

  function run(q) {
    if (inflight) inflight.abort();
    var ctl = new AbortController();
    inflight = ctl;
    panel.classList.add('loading');
    fetch(searchHref(q) + '&format=json&fast=1', {
      headers: { Accept: 'application/json' },
      signal: ctl.signal
    }).then(function (r) {
      if (!r.ok) throw new Error('search failed: ' + r.status);
      return r.json();
    }).then(function (data) {
      if (ctl !== inflight) return; // superseded by a newer keystroke
      panel.classList.remove('loading');
      render(data);
    }).catch(function (err) {
      if (err && err.name === 'AbortError') return;
      if (ctl !== inflight) return;
      panel.classList.remove('loading');
      // Surface it: a silently empty palette reads as "nothing matched".
      msg('cmdk-error', 'Search is unavailable right now.');
    });
  }

  input.addEventListener('input', function () {
    var q = input.value.trim();
    allLink.setAttribute('href', searchHref(q));
    if (timer) clearTimeout(timer);
    if (q.length < MIN_CHARS) {
      if (inflight) { inflight.abort(); inflight = null; }
      panel.classList.remove('loading');
      render(null);
      return;
    }
    timer = setTimeout(function () { run(q); }, DEBOUNCE_MS);
  });

  input.addEventListener('keydown', function (e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); setSelected(sel + 1); return; }
    if (e.key === 'ArrowUp') { e.preventDefault(); setSelected(sel - 1); return; }
    if (e.key === 'Enter') {
      e.preventDefault();
      if (sel >= 0 && rows[sel]) { go(rows[sel].href, e.metaKey || e.ctrlKey); }
      else { go(searchHref(input.value.trim()), e.metaKey || e.ctrlKey); }
      return;
    }
    // Trap Tab inside the dialog. Only two stops exist (the input and the
    // see-all link), so either direction lands on the other one.
    if (e.key === 'Tab') {
      e.preventDefault();
      allLink.focus();
    }
  });

  allLink.addEventListener('keydown', function (e) {
    if (e.key === 'Tab') { e.preventDefault(); input.focus(); }
  });

  // Backdrop click closes; a click inside the panel must not.
  overlay.addEventListener('click', function (e) {
    if (!panel.contains(e.target)) close();
  });

  function typingTarget(el) {
    if (!el) return false;
    var tag = el.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
  }

  /* Capture phase: Escape must reach us before the detail pane's document-level
     handler (layout.html), or closing the palette over an open peek pane would
     also tear the pane down. stopPropagation keeps that from happening. */
  document.addEventListener('keydown', function (e) {
    if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
      e.preventDefault();
      if (isOpen()) close(); else open();
      return;
    }
    if (isOpen() && e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
      return;
    }
    if (e.key === '/' && !isOpen() && !typingTarget(e.target) &&
        !e.metaKey && !e.ctrlKey && !e.altKey) {
      e.preventDefault();
      open();
    }
  }, true);
})();
