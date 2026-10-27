import { formatDateTime, formatNumber } from "@/i18n/format";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";

/**
 * Links keep arriving (instance-level webhook) although the extension is not on
 * in this workspace: the line under "Available" says so, else it reads as unused.
 */
export function receivingNote(e: CatalogEntry): { key: string; values: Record<string, string> } | null {
  if (e.status?.enabled || !e.activity || e.activity.links <= 0) return null;
  return {
    key: "installed.receiving",
    values: { count: formatNumber(e.activity.links), last: e.activity.last_event_at ? formatDateTime(e.activity.last_event_at) : "-" },
  };
}

/** The workspace-level settings schema of an extension, or null when it has none. */
export function workspaceSettingsSchema(e: CatalogEntry): Record<string, unknown> | null {
  if (e.manifest.levels && e.manifest.levels.workspace === false) return null;
  const s = e.manifest.settings?.workspace;
  return s && typeof s === "object" ? (s as Record<string, unknown>) : null;
}
