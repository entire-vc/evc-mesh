import { useEffect, useLayoutEffect } from "react";
import { syncModeFromServer } from "@/core/themes/mode";
import { applyWorkspaceTheme, clearWorkspaceTheme } from "@/core/themes/runtime";

/**
 * Applies the theme of the current workspace (tokens, CSS, views). Sits next to
 * useDynamicFavicon in AppLayout. The slug comes from the URL only (what the
 * pre-paint script also reads): the store is updated by a later passive effect, and a layout
 * effect keeps the cached styles in the same commit as the navigation, before the browser
 * paints. A route without a workspace slug shows the instance theme.
 *
 * `ready` is false until the session is established: the access token lives in memory and arrives
 * after the shell mounts, and GET /theme?workspace= answers 401 without it. Until then the
 * pre-paint cached styles stay as they are.
 */
export function useWorkspaceTheme(urlSlug?: string, ready = true) {
  const slug = urlSlug || null;
  useLayoutEffect(() => {
    if (!ready) return;
    if (!slug) {
      clearWorkspaceTheme();
      return;
    }
    let stale = false;
    void applyWorkspaceTheme(slug, () => stale);
    return () => {
      stale = true;
    };
  }, [slug, ready]);
  // Unmounting means the app shell is gone (sign-out to /login): drop the workspace theme with it.
  useLayoutEffect(() => clearWorkspaceTheme, []);
}

/** Colour mode: takes the server's choice once signed in (the OS watcher runs app-wide from main.tsx). */
export function useColorMode(ready = true) {
  useEffect(() => {
    if (ready) void syncModeFromServer();
  }, [ready]);
}
