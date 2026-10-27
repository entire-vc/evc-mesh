import type { CatalogEntry } from "@/hooks/useExtensionCatalog";
import { RUNNER_PRODUCT_ID, RUNNER_SERVER_ID } from "@/lib/runner-product";

/** The Installed tab opens the settings dialog of the extension named by this query parameter. */
export const OPEN_SETTINGS_PARAM = "settings";

/** Extensions whose settings live in their own section of Mesh, not in the extension dialog: the Runner, server side and program. */
const OWN_SECTIONS: Record<string, string> = { [RUNNER_SERVER_ID]: "runner", [RUNNER_PRODUCT_ID]: "runner" };

type RouteEntry = Pick<CatalogEntry, "id" | "kind" | "hidden" | "managedElsewhere">;

/**
 * Where "Open settings" leads for an extension: the Runner to its own section, any other extension
 * (a theme too, П8) to its own settings dialog on the Installed tab.
 * Null when the extension has no settings page Mesh can open (hidden or written elsewhere).
 */
export function extensionSettingsPath(e: RouteEntry, wsSlug: string): string | null {
  const own = OWN_SECTIONS[e.id];
  if (own) return `/w/${wsSlug}/${own}`;
  if (e.hidden || (e.managedElsewhere && e.kind !== "theme")) return null;
  return `/w/${wsSlug}/extensions?tab=installed&${OPEN_SETTINGS_PARAM}=${encodeURIComponent(e.id)}`;
}

/** The generic Connect wizard cannot write this extension's settings: the server refuses them. */
export function connectWizardRefused(e: Pick<CatalogEntry, "hidden" | "managedElsewhere">): boolean {
  return e.hidden || e.managedElsewhere;
}
