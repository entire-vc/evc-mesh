import { useEffect, useState } from "react";
import type { ThemeMode } from "@/lib/extension-frame-protocol";

/** CSS variables of the Mesh theme a widget receives in `mesh.context` and `mesh.theme`. */
export const WIDGET_THEME_TOKENS = [
  "--background",
  "--foreground",
  "--card",
  "--card-foreground",
  "--popover",
  "--popover-foreground",
  "--primary",
  "--primary-foreground",
  "--secondary",
  "--secondary-foreground",
  "--muted",
  "--muted-foreground",
  "--accent",
  "--accent-foreground",
  "--destructive",
  "--border",
  "--input",
  "--ring",
  "--radius",
  "--font-sans",
] as const;

export interface WidgetTheme {
  mode: ThemeMode;
  tokens: Record<string, string>;
}

/** The theme as painted right now: mode from `<html class="dark">`, token values as computed. */
export function readWidgetTheme(): WidgetTheme {
  const root = document.documentElement;
  const cs = getComputedStyle(root);
  const tokens: Record<string, string> = {};
  for (const name of WIDGET_THEME_TOKENS) {
    const v = cs.getPropertyValue(name).trim();
    if (v) tokens[name] = v;
  }
  return { mode: root.classList.contains("dark") ? "dark" : "light", tokens };
}

/** Same value for the same theme, so an unrelated DOM change does not look like a theme change. */
export function themeKey(t: WidgetTheme): string {
  return t.mode + JSON.stringify(t.tokens);
}

/** The painted theme; follows the light/dark switch and a theme's token stylesheet being swapped. */
export function useWidgetTheme(): WidgetTheme {
  const [theme, setTheme] = useState<WidgetTheme>(readWidgetTheme);
  useEffect(() => {
    let last = themeKey(theme);
    const check = () => {
      const next = readWidgetTheme();
      const k = themeKey(next);
      if (k !== last) {
        last = k;
        setTheme(next);
      }
    };
    const mo = new MutationObserver(check);
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "style", "data-theme"] });
    mo.observe(document.head, { childList: true, subtree: true, characterData: true });
    return () => mo.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return theme;
}
