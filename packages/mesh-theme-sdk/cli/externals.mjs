// The modules a theme bundle never carries: Mesh maps them to its own single copy through the import
// map (ADR D10, web/src/core/themes/shared.ts). Keep in step with web/scripts/assert-single-react.mjs.
export const EXTERNALS = ["react", "react-dom", "react/jsx-runtime", "react-i18next", "@entire-vc/mesh-theme-sdk"];

const CORE = /\.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE\s*=[^=]/;
const DOM = /\.__DOM_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE\s*=[^=]/;

/** What is wrong with a built bundle's text: an embedded React / react-dom, or a bare import the import map lacks. */
export function checkBundleText(src) {
  const bad = [];
  if (CORE.test(src) || /Symbol\.for\(["'`]react\.transitional\.element["'`]\)/.test(src)) bad.push("embeds a copy of React");
  if (DOM.test(src)) bad.push("embeds a copy of react-dom");
  const specs = new Set();
  for (const m of src.matchAll(/(?:\bfrom\s*|\bimport\s*\(?\s*)["']([^"']+)["']/g)) specs.add(m[1]);
  for (const s of specs) {
    if (s.startsWith(".") || s.startsWith("/") || /^https?:/.test(s)) continue;
    if (!EXTERNALS.includes(s)) bad.push(`imports "${s}", which is not in the import map`);
  }
  return bad;
}
