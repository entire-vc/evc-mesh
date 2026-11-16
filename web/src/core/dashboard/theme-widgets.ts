import { useSyncExternalStore } from "react";
import type { WidgetDescriptorV1 } from "./types";

// The live theme's own dashboard widgets (descriptors from its bundle, checked by core/themes/bundle.ts `ownWidgets`).
// The theme runtime sets them with the views and clears them when the theme goes; the dashboard lists them next to the core's.
const NONE: readonly WidgetDescriptorV1[] = [];
let current: readonly WidgetDescriptorV1[] = NONE;
const listeners = new Set<() => void>();

export function setThemeWidgets(next: readonly WidgetDescriptorV1[]): void {
  if (next.length === 0 && current === NONE) return;
  current = next.length ? next : NONE;
  listeners.forEach((l) => l());
}

export function useThemeWidgets(): readonly WidgetDescriptorV1[] {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
    () => current,
  );
}

/** Slot of a widget view: a core id is its own name, a theme's `<theme-id>.<name>` is `dashboard.widget.<name>`. */
export const widgetSlot = (id: string): string => `dashboard.widget.${id.includes(".") ? id.slice(id.indexOf(".") + 1) : id}`;

// Major of the dashboard.layout contract the live theme's view declared (`^1` -> 1). No theme view = the core's own (2).
let layoutMajor = 2;
const layoutListeners = new Set<() => void>();

export function setLayoutMajor(next: number): void {
  if (next === layoutMajor) return;
  layoutMajor = next;
  layoutListeners.forEach((l) => l());
}

export function useLayoutMajor(): number {
  return useSyncExternalStore(
    (l) => {
      layoutListeners.add(l);
      return () => layoutListeners.delete(l);
    },
    () => layoutMajor,
    () => layoutMajor,
  );
}

/** `^1` / `^1.2.0` -> 1; anything else -> 2 (the core's own major). */
export const majorOf = (range: string | undefined): number => {
  const m = /^\^(\d+)/.exec(range ?? "");
  return m ? Number(m[1]) : 2;
};
