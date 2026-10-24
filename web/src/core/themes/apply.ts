// DOM side of a theme: token <style> after brandkit.css, theme CSS in the
// `mesh-theme` layer (ADR D4 — `theme` is Tailwind's own layer).
export const TOKENS_STYLE_ID = "mesh-theme-tokens";
export const CSS_STYLE_ID = "mesh-theme-css";
const PRE_PAINT_ID = "mesh-instance-theme";

function put(id: string, css: string): void {
  let el = document.getElementById(id) as HTMLStyleElement | null;
  if (!css) {
    el?.remove();
    return;
  }
  if (!el) {
    el = document.createElement("style");
    el.id = id;
    document.head.appendChild(el);
  } else if (el.textContent === css) {
    return;
  }
  el.textContent = css;
}

/**
 * `:root{…} .dark{…}` -> `html:root{…} html.dark{…}`: outranks brandkit.css at any
 * source order, which the pre-paint script cannot control (its <style> precedes
 * Vite's stylesheet link). Keep in step with public/instance-theme.js.
 */
export function strengthenTokens(css: string): string {
  return css
    .replace(/(^|\})(\s*):root\s*\{/g, "$1$2html:root{")
    .replace(/(^|\})(\s*)\.dark\s*\{/g, "$1$2html.dark{");
}

/** Wraps unlayered CSS so it cannot bypass the layer order; the server already does, this is the belt. */
export function inMeshThemeLayer(css: string): string {
  return /@layer\s+mesh-theme\b/.test(css) ? css : `@layer mesh-theme {\n${css}\n}`;
}

export function applyThemeStyles(tokensCss: string, themeCss: string): void {
  put(TOKENS_STYLE_ID, strengthenTokens(tokensCss));
  put(CSS_STYLE_ID, themeCss ? inMeshThemeLayer(themeCss) : "");
  // The pre-paint script's copy is superseded once this one is in.
  document.getElementById(PRE_PAINT_ID)?.remove();
}

/** Leaves the workspace: its tokens and CSS go, the instance theme (what the pre-paint script cached) comes back. */
export function clearWorkspaceStyles(): void {
  put(TOKENS_STYLE_ID, "");
  put(CSS_STYLE_ID, "");
  if (document.getElementById(PRE_PAINT_ID)) return;
  try {
    const t = JSON.parse(localStorage.getItem("mesh-instance-theme") || "null");
    if (t && typeof t.tokens_css === "string" && t.tokens_css) put(PRE_PAINT_ID, strengthenTokens(t.tokens_css));
  } catch {
    /* no cached instance theme: brandkit.css defaults */
  }
}
