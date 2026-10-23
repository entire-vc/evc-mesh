// The instance theme outside the app: the sign-in, registration and invitation
// screens, and the browser chrome colour (<meta name="theme-color">).
//
// A plain script (not part of the Vite bundle: it must run before the first
// frame, and it costs the JS budget nothing). GET /api/v1/theme is public, so
// it works before anyone is signed in. The last answer is cached in
// localStorage and applied at once; a fresh one is fetched and applied when it
// arrives, so only a first visit can paint mesh-default. Token CSS goes onto
// pre-auth screens only; inside the signed-in app just the chrome colour.
(function () {
  var KEY = "mesh-instance-theme";
  var STYLE_ID = "mesh-instance-theme";
  var PRE_AUTH = /^\/(login|register|accept-invite|connect)(\/|$)/;

  function valid(o) {
    return !!o && typeof o.tokens_css === "string" && !!o.brand && typeof o.brand.theme_color === "string";
  }

  function apply(t) {
    var old = document.getElementById(STYLE_ID);
    if (old) old.remove();
    if (PRE_AUTH.test(location.pathname) && t.tokens_css) {
      var s = document.createElement("style");
      s.id = STYLE_ID;
      s.textContent = t.tokens_css;
      document.head.appendChild(s);
    }
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) {
      var dark = document.documentElement.classList.contains("dark");
      meta.content = dark ? t.brand.theme_color_dark : t.brand.theme_color;
    }
  }

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
