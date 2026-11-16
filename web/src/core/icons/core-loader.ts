// The core icon set is a lazy chunk (core-icons.ts, ~230 lucide icons): the main bundle only carries this
// loader. Drawing an `icon:<name>` that the live theme does not supply, or opening the picker, loads it once.
import { useEffect, useSyncExternalStore } from "react";
import type { IconComponent } from "./core-icons";

export type CoreIcons = Record<string, IconComponent>;

let loaded: CoreIcons | null = null;
let pending: Promise<CoreIcons> | null = null;
const listeners = new Set<() => void>();

export function seedCoreIcons(icons: CoreIcons): void {
  if (loaded) return;
  loaded = icons;
  listeners.forEach((l) => l());
}

export function loadCoreIcons(): Promise<CoreIcons> {
  if (loaded) return Promise.resolve(loaded);
  pending ??= import("./core-icons").then(
    (m) => {
      seedCoreIcons(m.CORE_ICONS);
      return m.CORE_ICONS;
    },
    (e: unknown) => {
      pending = null; // retried on the next draw
      throw e;
    },
  );
  return pending;
}

/** The core icons once loaded, null until then; `enabled=false` never starts the load. */
export function useCoreIcons(enabled: boolean): CoreIcons | null {
  const icons = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => loaded,
    () => loaded,
  );
  useEffect(() => {
    if (enabled && !loaded) void loadCoreIcons().catch(() => {});
  }, [enabled]);
  return icons;
}
