import { create } from "zustand";

interface ThemeState {
  /** Theme whose views the slots draw; `mesh-default` until a bundle is live. */
  viewsThemeId: string;
  settings: Record<string, unknown>;
  set: (s: Partial<Pick<ThemeState, "viewsThemeId" | "settings">>) => void;
}

export const useThemeStore = create<ThemeState>((set) => ({
  viewsThemeId: "mesh-default", // = DEFAULT_THEME_ID; not imported, the registry imports this store
  settings: {},
  set: (s) => set(s),
}));
