import { Component, createElement, useSyncExternalStore, type ReactNode } from "react";
import type { SlotProps, SlotView } from "./types";

export const DEFAULT_THEME_ID = "mesh-default";

// slot name -> theme id -> view. Until themes ship (T5) only `mesh-default`
// is registered.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyView = SlotView<any, any, any>;
const views = new Map<string, Map<string, AnyView>>();

// Bumped on every registration change so a mounted page re-resolves its views (a theme can
// register or drop an override after the board has mounted).
let registryVersion = 0;
const registryListeners = new Set<() => void>();
function registryChanged(): void {
  registryVersion += 1;
  registryListeners.forEach((l) => l());
}

/** Changes whenever a view is registered or an override is added/removed; use as a memo dependency. */
export function useSlotRegistryVersion(): number {
  return useSyncExternalStore(
    (listener) => {
      registryListeners.add(listener);
      return () => registryListeners.delete(listener);
    },
    () => registryVersion,
    () => registryVersion,
  );
}

export function registerSlotView(slot: string, themeId: string, view: AnyView): void {
  let byTheme = views.get(slot);
  if (!byTheme) {
    byTheme = new Map();
    views.set(slot, byTheme);
  }
  // Every page chunk registers the built-in theme at module scope; the same view again is not a change,
  // and announcing it would re-render the pages already mounted (one extra commit in task.open).
  if (byTheme.get(themeId) === view) return;
  byTheme.set(themeId, view);
  registryChanged();
}

// What a theme supplies on top of the built-in views, by slot name. The theme
// runtime (T5) fills it; an unthemed `resolveSlotView(slot)` reads it first.
const overrides = new Map<string, AnyView>();

/** Registers the built-in (mesh-default) view of a slot. */
export function registerDefaultSlotView(slot: string, view: AnyView): void {
  registerSlotView(slot, DEFAULT_THEME_ID, view);
}

/** The active theme's view for a slot; what it does not register falls back to the default. Returns a disposer. */
export function registerSlotViewOverride(slot: string, view: AnyView): () => void {
  overrides.set(slot, view);
  registryChanged();
  return () => {
    if (overrides.get(slot) === view) {
      overrides.delete(slot);
      registryChanged();
    }
  };
}

export function resolveSlotView(slot: string, themeId?: string): AnyView {
  const byTheme = views.get(slot);
  const view =
    themeId === undefined
      ? (overrides.get(slot) ?? byTheme?.get(DEFAULT_THEME_ID))
      : (byTheme?.get(themeId) ?? byTheme?.get(DEFAULT_THEME_ID));
  if (!view) throw new Error(`no view registered for slot "${slot}"`);
  return view;
}

const NO_SETTINGS: Record<string, unknown> = {};
let settings: Record<string, unknown> = NO_SETTINGS;
const settingsListeners = new Set<() => void>();

/** Active theme's values for the slot settings (e.g. `card_fields`); empty = every view's defaults. */
export function resolveThemeSettings(): Record<string, unknown> {
  return settings;
}

/** The theme runtime (T5) pushes the active theme's settings here; mounted boards re-draw at once. `{}` resets. */
export function setThemeSettings(next: Record<string, unknown>): void {
  settings = Object.keys(next).length ? next : NO_SETTINGS;
  settingsListeners.forEach((l) => l());
}

export function subscribeThemeSettings(listener: () => void): () => void {
  settingsListeners.add(listener);
  return () => settingsListeners.delete(listener);
}

/** `resolveThemeSettings` as a hook: the component re-renders when the settings change. */
export function useThemeSettings(): Record<string, unknown> {
  return useSyncExternalStore(subscribeThemeSettings, resolveThemeSettings, resolveThemeSettings);
}

// A view that throws while rendering must not take the page down: fall back
// to the `mesh-default` view of the same slot (CT-common-07).
export class SlotBoundary extends Component<
  { fallback: ReactNode; children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

export function Slot<D, A, S>({
  name,
  themeId = DEFAULT_THEME_ID,
  ...props
}: SlotProps<D, A, S> & { name: string; themeId?: string }) {
  const View = resolveSlotView(name, themeId);
  const Default = resolveSlotView(name, DEFAULT_THEME_ID);
  return (
    <SlotBoundary fallback={<Default {...props} />}>
      <View {...props} />
    </SlotBoundary>
  );
}

const wrapped = new Map<string, { view: AnyView; wrapper: AnyView }>();

/**
 * `view` as a component that falls back to the `mesh-default` view of the same slot when it throws
 * while rendering (CT-common-07). The wrapper keeps its identity while `view` does, so a re-render
 * does not remount the subtree. The built-in view itself is returned as is.
 */
export function withSlotFallback(slot: string, view: AnyView): AnyView {
  const Default = resolveSlotView(slot, DEFAULT_THEME_ID);
  if (view === Default) return view;
  const hit = wrapped.get(slot);
  if (hit && hit.view === view) return hit.wrapper;
  const wrapper: AnyView = (props) => (
    <SlotBoundary fallback={<Default {...props} />}>
      {createElement(view, props)}
    </SlotBoundary>
  );
  wrapped.set(slot, { view, wrapper });
  return wrapper;
}
