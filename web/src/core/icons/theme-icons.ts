// Icons a theme supplies: `icons` (kebab-case name -> component) and `icons_meta` (search keywords)
// exported next to `views` from its bundle (core/themes/bundle.ts hands them over). The set of the
// theme that is live decides how `icon:<name>` is drawn; a name it does not have falls to the core set.
import { useSyncExternalStore } from "react";
import type { ComponentType } from "react";
import { ICON_NAME } from "./value";

export type ThemeIconComponent = ComponentType<{ className?: string }>;
/** Keywords of one icon, per language; a string is split on spaces. */
export type ThemeIconMeta = { en?: string | string[]; ru?: string | string[] };

export interface ThemeIconSet {
  icons: Record<string, ThemeIconComponent>;
  meta: Record<string, ThemeIconMeta>;
}

const sets = new Map<string, ThemeIconSet>();
let version = 0;
const listeners = new Set<() => void>();
function changed(): void {
  version += 1;
  listeners.forEach((l) => l());
}

/** Keeps the entries that are valid (kebab-case name, function component); returns the names dropped. */
export function registerThemeIcons(
  themeId: string,
  icons: unknown,
  meta: unknown,
): { dropped: string[] } {
  const dropped: string[] = [];
  const ok: Record<string, ThemeIconComponent> = {};
  if (icons && typeof icons === "object") {
    for (const [name, c] of Object.entries(icons as Record<string, unknown>)) {
      if (ICON_NAME.test(name) && (typeof c === "function" || (typeof c === "object" && c !== null))) {
        ok[name] = c as ThemeIconComponent;
      } else dropped.push(name);
    }
  }
  const m: Record<string, ThemeIconMeta> = {};
  if (meta && typeof meta === "object") {
    for (const [name, v] of Object.entries(meta as Record<string, unknown>)) {
      if (name in ok && v && typeof v === "object") m[name] = v as ThemeIconMeta;
    }
  }
  if (Object.keys(ok).length === 0) {
    clearThemeIcons(themeId);
  } else {
    sets.set(themeId, { icons: ok, meta: m });
    changed();
  }
  return { dropped };
}

export function clearThemeIcons(themeId: string): void {
  if (sets.delete(themeId)) changed();
}

export function getThemeIcons(themeId: string): ThemeIconSet | undefined {
  return sets.get(themeId);
}

/** Re-renders the caller whenever any theme's icon set is registered or dropped. */
export function useThemeIconsVersion(): number {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => version,
    () => version,
  );
}
