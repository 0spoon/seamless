/* Console data navigation: filters, sorts, time windows, GET searches, and
   owner mutation forms stay inside the current document. The server remains
   the single rendering source; this client fetches the next server-rendered
   page and morphs only the changed DOM into .main. Real links/forms remain as
   the no-JS fallback.

   window.SeamConsole also exposes the same morph to the SSE live refresher so
   every data refresh shares one no-reload path. */
(function () {
  'use strict';

  var inflight = null;
  var requestSeq = 0;
  var toastTimer = null;
  var currentViewURL = new URL(location.href);

  function imported(node) { return document.importNode(node, true); }
  function inserted(node) {
    var copy = imported(node);
    if (copy.nodeType === 1) copy.classList.add('live-in');
    return copy;
  }
  function keyOf(node) {
    return node.nodeType === 1 && node.id ? node.nodeName + '#' + node.id : null;
  }
  function syncAttrs(from, to) {
    var targetAttrs = to.attributes;
    var currentAttrs = from.attributes;
    var i;
    var attr;
    for (i = 0; i < targetAttrs.length; i++) {
      attr = targetAttrs[i];
      if (from.getAttribute(attr.name) !== attr.value) from.setAttribute(attr.name, attr.value);
    }
    for (i = currentAttrs.length - 1; i >= 0; i--) {
      attr = currentAttrs[i];
      if (to.hasAttribute(attr.name)) continue;
      if (attr.name === 'open' && from.nodeName === 'DETAILS') continue;
      from.removeAttribute(attr.name);
    }
  }
  function syncControl(from, to) {
    if (from.nodeName === 'INPUT') {
      if (from.type !== 'file' && from.value !== to.value) from.value = to.value;
      if ((from.type === 'checkbox' || from.type === 'radio') && from.checked !== to.checked) from.checked = to.checked;
    } else if (from.nodeName === 'TEXTAREA') {
      if (from.value !== to.value) from.value = to.value;
    } else if (from.nodeName === 'SELECT') {
      if (from.value !== to.value) from.value = to.value;
    }
  }
  function morphNode(from, to) {
    if (from.nodeType !== to.nodeType ||
        (from.nodeType === 1 && from.nodeName !== to.nodeName)) {
      from.replaceWith(inserted(to));
      return;
    }
    if (from.nodeType !== 1) {
      if (from.nodeValue !== to.nodeValue) from.nodeValue = to.nodeValue;
      return;
    }
    if (from.hasAttribute('data-live-skip')) return;
    syncAttrs(from, to);
    morphChildren(from, to);
    syncControl(from, to);
  }
  function morphChildren(from, to) {
    var keyed = {};
    var old;
    for (old = from.firstChild; old; old = old.nextSibling) {
      var key = keyOf(old);
      if (key) keyed[key] = old;
    }

    var oldChild = from.firstChild;
    var newChild = to.firstChild;
    while (newChild) {
      var nextNew = newChild.nextSibling;
      var newKey = keyOf(newChild);
      if (newKey && keyed[newKey]) {
        var match = keyed[newKey];
        delete keyed[newKey];
        if (match === oldChild) oldChild = oldChild.nextSibling;
        else from.insertBefore(match, oldChild);
        morphNode(match, newChild);
      } else if (oldChild) {
        var oldKey = keyOf(oldChild);
        if (oldKey && keyed[oldKey]) {
          from.insertBefore(inserted(newChild), oldChild);
        } else if (!oldKey && oldChild.nodeType === newChild.nodeType &&
                   (oldChild.nodeType !== 1 || oldChild.nodeName === newChild.nodeName)) {
          morphNode(oldChild, newChild);
          oldChild = oldChild.nextSibling;
        } else {
          from.insertBefore(inserted(newChild), oldChild);
        }
      } else {
        from.appendChild(inserted(newChild));
      }
      newChild = nextNew;
    }
    while (oldChild) {
      var nextOld = oldChild.nextSibling;
      from.removeChild(oldChild);
      oldChild = nextOld;
    }
    Object.keys(keyed).forEach(function (key) {
      if (keyed[key].parentNode === from) from.removeChild(keyed[key]);
    });
  }

  function stripAnimations() {
    setTimeout(function () {
      document.querySelectorAll('.live-in, nav.nav .count.bump').forEach(function (el) {
        el.classList.remove('live-in', 'bump');
      });
    }, 600);
  }

  function patchPage(doc, source) {
    var freshMain = doc.querySelector('.main');
    var currentMain = document.querySelector('.main');
    var freshNav = doc.querySelector('nav.nav');
    var currentNav = document.querySelector('nav.nav');
    if (!freshMain || !currentMain || !freshNav || !currentNav) return false;

    try {
      morphNode(currentMain, freshMain);
    } catch (error) {
      // A morph edge case still stays inside this document. Replace only the
      // view contents with the fully rendered response; never reload the page.
      var children = Array.prototype.slice.call(freshMain.childNodes).map(function (node) { return imported(node); });
      currentMain.replaceChildren.apply(currentMain, children);
    }
    var freshCounts = freshNav.querySelectorAll('.count');
    var currentCounts = currentNav.querySelectorAll('.count');
    if (freshCounts.length === currentCounts.length) {
      for (var i = 0; i < currentCounts.length; i++) {
        if (currentCounts[i].textContent === freshCounts[i].textContent) continue;
        currentCounts[i].textContent = freshCounts[i].textContent;
        currentCounts[i].classList.remove('bump');
        void currentCounts[i].offsetWidth;
        currentCounts[i].classList.add('bump');
      }
    }
    var freshLinks = freshNav.querySelectorAll('a');
    var currentLinks = currentNav.querySelectorAll('a');
    if (freshLinks.length === currentLinks.length) {
      for (var j = 0; j < currentLinks.length; j++) {
        currentLinks[j].classList.toggle('active', freshLinks[j].classList.contains('active'));
      }
    }
    if (doc.title) document.title = doc.title;
    try {
      if (window.IX && window.IX.enhance) window.IX.enhance(currentMain);
    } catch (e) {}
    document.dispatchEvent(new CustomEvent('seam:content-updated', {
      detail: { source: source || 'data' }
    }));
    stripAnimations();
    return true;
  }

  function flash(message, tone) {
    var toast = document.getElementById('live-toast');
    if (!toast) return;
    toast.textContent = message;
    toast.hidden = false;
    toast.classList.toggle('error', tone === 'error');
    toast.classList.remove('show');
    void toast.offsetWidth;
    toast.classList.add('show');
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(function () {
      toast.classList.remove('show');
      setTimeout(function () { toast.hidden = true; }, 300);
    }, 2600);
  }

  function setBusy(on) {
    var main = document.querySelector('.main');
    if (!main) return;
    if (on) main.setAttribute('aria-busy', 'true');
    else main.removeAttribute('aria-busy');
  }

  function canonicalURL(doc, fallback) {
    var auto = doc.querySelector('.main .lib-layout[data-auto-url]');
    var raw = auto && auto.getAttribute('data-auto-url');
    try { return new URL(raw || fallback, location.origin).href; }
    catch (e) { return fallback; }
  }

  function load(rawURL, options) {
    options = options || {};
    var target;
    try { target = new URL(rawURL, location.href); }
    catch (e) { flash('Could not open that view.', 'error'); return Promise.resolve(false); }

    var method = (options.method || 'GET').toUpperCase();
    var mutation = method !== 'GET';
    if (inflight && inflight.mutation) {
      flash('Another update is still in progress.', 'error');
      return Promise.resolve(false);
    }
    if (inflight && inflight.controller) inflight.controller.abort();
    var controller = window.AbortController ? new AbortController() : null;
    inflight = { controller: controller, mutation: mutation };
    var seq = ++requestSeq;
    setBusy(true);

    var headers = { Accept: 'text/html', 'X-Seam-Partial': '1' };
    return fetch(target.href, {
      method: method,
      body: method === 'GET' || method === 'HEAD' ? undefined : options.body,
      credentials: 'same-origin',
      headers: headers,
      signal: controller ? controller.signal : undefined
    }).then(function (response) {
      var finalURL = new URL(response.url || target.href, location.href);
      if (finalURL.pathname === '/console/login') {
        location.assign(finalURL.href);
        return null;
      }
      if (!response.ok) throw new Error('http ' + response.status);
      return response.text().then(function (html) {
        if (seq !== requestSeq) return false;
        var doc = new DOMParser().parseFromString(html, 'text/html');
        if (!doc.querySelector('.sidebar') || !patchPage(doc, options.source || 'query')) {
          throw new Error('invalid console document');
        }
        var nextURL = canonicalURL(doc, finalURL.href);
        if (options.history !== 'none') {
          var state = { seamView: 'query' };
          if (options.history === 'replace') history.replaceState(state, '', nextURL);
          else history.pushState(state, '', nextURL);
        }
        currentViewURL = new URL(nextURL, location.href);
        if (currentViewURL.hash) {
          try {
            var anchor = document.getElementById(decodeURIComponent(currentViewURL.hash.slice(1)));
            if (anchor) anchor.scrollIntoView({ block: 'start' });
          } catch (e) {}
        }
        return true;
      });
    }).catch(function (error) {
      if (error && error.name === 'AbortError') return false;
      flash('Could not update this view. The current data is unchanged.', 'error');
      document.dispatchEvent(new CustomEvent('seam:content-error', { detail: { error: error } }));
      return false;
    }).finally(function () {
      if (seq !== requestSeq) return;
      inflight = null;
      setBusy(false);
    });
  }

  function queryLink(anchor) {
    return anchor.hasAttribute('data-seam-query') || !!anchor.closest('[data-seam-query]');
  }

  document.addEventListener('click', function (event) {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button === 1) return;
    var anchor = event.target.closest ? event.target.closest('a[href]') : null;
    if (!anchor || !queryLink(anchor) || anchor.target || anchor.hasAttribute('download')) return;
    var target;
    try { target = new URL(anchor.href, location.href); } catch (e) { return; }
    if (target.origin !== location.origin) return;
    if (target.pathname === location.pathname && target.search === location.search && target.hash) return;
    event.preventDefault();
    if (target.href === location.href) return;
    load(target.href, { history: 'push', source: 'query' });
  });

  document.addEventListener('submit', function (event) {
    var form = event.target;
    if (event.defaultPrevented || !form.matches) return;
    var submitter = event.submitter;
    var method = (submitter && submitter.hasAttribute('formmethod') ? submitter.formMethod : form.method || 'get').toLowerCase();
    var query = form.matches('form[data-seam-query]');
    var mutation = method === 'post' && !!form.closest('.main');
    if ((method === 'get' && !query) || (method !== 'get' && !mutation)) return;
    event.preventDefault();

    var action = submitter && submitter.hasAttribute('formaction') ? submitter.formAction : form.action || location.href;
    var target = new URL(action, location.href);
    var data;
    try { data = new FormData(form, submitter); }
    catch (e) {
      data = new FormData(form);
      if (submitter && submitter.name) data.append(submitter.name, submitter.value);
    }
    if (method === 'get') {
      target.search = '';
      data.forEach(function (value, key) {
        if (typeof value === 'string') target.searchParams.append(key, value);
      });
      load(target.href, { history: 'push', source: 'query' });
      return;
    }

    var body;
    if ((form.enctype || '').toLowerCase() === 'multipart/form-data') {
      body = data;
    } else {
      body = new URLSearchParams();
      data.forEach(function (value, key) {
        if (typeof value === 'string') body.append(key, value);
      });
    }
    load(target.href, { method: 'POST', body: body, history: 'push', source: 'mutation' });
  });

  document.addEventListener('change', function (event) {
    var form = event.target.closest ? event.target.closest('form[data-seam-query][data-seam-auto-submit]') : null;
    if (!form) return;
    if (typeof form.requestSubmit === 'function') form.requestSubmit();
  });

  function surfaceBase() {
    var reader = document.getElementById('lib-reader');
    return reader && reader.getAttribute('data-base');
  }
  function needsView(rawURL) {
    var target = new URL(rawURL, location.href);
    if (target.origin !== location.origin || target.pathname.indexOf('/console/') !== 0) return false;
    var base = surfaceBase();
    var sameSurface = base
      ? (target.pathname === base || target.pathname.indexOf(base + '/') === 0)
      : target.pathname === currentViewURL.pathname;
    return !sameSurface || target.search !== currentViewURL.search;
  }

  window.addEventListener('popstate', function () {
    if (needsView(location.href)) load(location.href, { history: 'none', source: 'history' });
  });

  try {
    var initial = history.state || {};
    initial.seamView = initial.seamView || 'page';
    history.replaceState(initial, '', location.href);
  } catch (e) {}

  window.SeamConsole = {
    flash: flash,
    load: load,
    morph: morphNode,
    needsView: needsView,
    patchPage: patchPage
  };
})();
