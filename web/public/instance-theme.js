// The instance theme outside the app: the sign-in, registration and invitation
// screens, and the browser chrome colour (<meta name="theme-color">).
//
// A plain script (not part of the Vite bundle: it must run before the first
// frame, and it costs the JS budget nothing). GET /api/v1/theme is public, so
// it works before anyone is signed in. The last answer is cached in
// localStorage and applied at once; a fresh one is fetched and applied when it
// arrives, so only a first visit can paint mesh-default. Token CSS goes onto
// every screen: the sign-in screens and the static shell take the instance
// theme; inside /w/<slug>/ the workspace's cached theme wins (T5), and React's
// ThemeProvider replaces either once it has the answer.
(function () {
  var KEY = "mesh-instance-theme";
  var STYLE_ID = "mesh-instance-theme";
  // Last theme per workspace slug, written by src/core/themes/cache.ts.
  var WS_PREFIX = "mesh-theme:";
  var WS_STYLE_ID = "mesh-theme-tokens";
  var WS_CSS_ID = "mesh-theme-css";

  // The server writes `:root{…} .dark{…}`. This style lands in <head> before
  // Vite's stylesheet link, so at equal specificity brandkit.css would win;
  // `html:root` / `html.dark` outrank it whatever the order (mirrors
  // src/core/themes/apply.ts).
  function strengthen(css) {
    return css.replace(/(^|\})(\s*):root\s*\{/g, "$1$2html:root{").replace(/(^|\})(\s*)\.dark\s*\{/g, "$1$2html.dark{");
  }

  function valid(o) {
    return !!o && typeof o.tokens_css === "string" && !!o.brand && typeof o.brand.theme_color === "string";
  }

  function apply(t) {
    var old = document.getElementById(STYLE_ID);
    if (old) old.remove();
    if (!wsPainted && !document.getElementById(WS_STYLE_ID) && t.tokens_css) {
      var s = document.createElement("style");
      s.id = STYLE_ID;
      s.textContent = strengthen(t.tokens_css);
      document.head.appendChild(s);
    }
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) {
      var dark = document.documentElement.classList.contains("dark");
      meta.content = dark ? t.brand.theme_color_dark : t.brand.theme_color;
    }
  }

  // Inside the app (/w/<slug>/...): paint the workspace's last theme before the
  // first frame; React's ThemeProvider takes over under the same style ids.
  // Without a cached one the instance tokens paint the static shell instead.
  function paintWorkspace() {
    var m = /^\/w\/([^/]+)/.exec(location.pathname);
    if (!m) return false;
    try {
      var c = JSON.parse(localStorage.getItem(WS_PREFIX + decodeURIComponent(m[1])) || "null");
      if (!c || typeof c.tokens_css !== "string") return false;
      var t = document.createElement("style");
      t.id = WS_STYLE_ID;
      t.textContent = strengthen(c.tokens_css);
      document.head.appendChild(t);
      if (c.css) {
        var l = document.createElement("style");
        l.id = WS_CSS_ID;
        l.textContent = /@layer\s+mesh-theme\b/.test(c.css) ? c.css : "@layer mesh-theme {\n" + c.css + "\n}";
        document.head.appendChild(l);
      }
      return true;
    } catch (e) {
      return false;
    }
  }
  var wsPainted = paintWorkspace();

  var current = null;
  try {
    current = JSON.parse(localStorage.getItem(KEY) || "null");
    if (!valid(current)) current = null;
  } catch (e) {}
  if (current) apply(current);

  // The light/dark switch moves the chrome colour with it.
  new MutationObserver(function () {
    if (current) apply(current);
  }).observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });

  fetch("/api/v1/theme")
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (t) {
      if (!valid(t)) return;
      current = t;
      apply(t);
      try { localStorage.setItem(KEY, JSON.stringify(t)); } catch (e) {}
    })
    .catch(function () {});
})();
