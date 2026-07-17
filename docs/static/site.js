/* thereisnospoon.org -- theme toggle, copy buttons, scroll reveals.
   No dependencies, no network, no state beyond localStorage("theme"). */
(function () {
  "use strict";

  /* theme toggle: explicit choice wins; otherwise follow the OS */
  var root = document.documentElement;
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

  /* os switch: the head script already set data-os from the UA; a pick
     overrides it everywhere on the page and persists. aria-pressed mirrors
     data-os for assistive tech; the visual state is pure CSS off <html>. */
  function syncOsButtons() {
    document.querySelectorAll("[data-os-pick]").forEach(function (btn) {
      btn.setAttribute("aria-pressed", btn.dataset.osPick === root.dataset.os ? "true" : "false");
    });
  }
  syncOsButtons();
  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest("[data-os-pick]");
    if (!btn) return;
    root.dataset.os = btn.dataset.osPick;
    try { localStorage.setItem("os", btn.dataset.osPick); } catch (e) { /* private mode */ }
    syncOsButtons();
  });

  /* copy buttons: copy the nearest [data-copy] text */
  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest(".copy-btn");
    if (!btn) return;
    var scope = btn.closest(".install-pill, .code");
    var src = scope && scope.querySelector("[data-copy]");
    if (!src) return;
    navigator.clipboard.writeText(src.getAttribute("data-copy")).then(function () {
      btn.textContent = "copied";
      btn.classList.add("ok");
      setTimeout(function () {
        btn.textContent = "copy";
        btn.classList.remove("ok");
      }, 1600);
    });
  });

  /* reveal on scroll */
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) {
          e.target.classList.add("in");
          io.unobserve(e.target);
        }
      });
    }, { rootMargin: "0px 0px -8% 0px" });
    document.querySelectorAll(".rv").forEach(function (el) { io.observe(el); });
  } else {
    document.querySelectorAll(".rv").forEach(function (el) { el.classList.add("in"); });
  }
})();
