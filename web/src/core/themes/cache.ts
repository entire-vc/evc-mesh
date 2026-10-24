// Last theme per workspace slug, kept so index-time script (public/instance-theme.js)
// can paint it before React. Key format is shared with that script.
export const THEME_CACHE_PREFIX = "mesh-theme:";

export interface CachedTheme {
  hash: string;
  tokens_css: string;
  css: string;
  /** The theme's colour modes, so a one-mode theme forces its mode from the first frame. */
  modes?: string[];
}

export function readThemeCache(slug: string): CachedTheme | null {
  try {
    const v = JSON.parse(localStorage.getItem(THEME_CACHE_PREFIX + slug) || "null");
    return v && typeof v.hash === "string" && typeof v.tokens_css === "string" ? { css: "", ...v } : null;
  } catch {
    return null;
  }
}

export function writeThemeCache(slug: string, t: CachedTheme): void {
  try {
    localStorage.setItem(THEME_CACHE_PREFIX + slug, JSON.stringify(t));
  } catch {
    /* quota or locked storage: next visit just paints a frame later */
  }
}
