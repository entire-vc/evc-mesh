import { useSyncExternalStore } from "react";
import type { WidgetDescriptor } from "./types";

// The live theme's own dashboard widgets (descriptors from its bundle, checked by core/themes/bundle.ts `ownWidgets`).
// The theme runtime sets them with the views and clears them when the theme goes; the dashboard lists them next to the core's.
const NONE: readonly WidgetDescriptor[] = [];
let current: readonly WidgetDescriptor[] = NONE;
const listeners = new Set<() => void>();

export function setThemeWidgets(next: readonly WidgetDescriptor[]): void {
  if (next.length === 0 && current === NONE) return;
  current = next.length ? next : NONE;
  listeners.forEach((l) => l());
}

export function useThemeWidgets(): readonly WidgetDescriptor[] {
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
