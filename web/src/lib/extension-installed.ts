import { formatDateTime, formatNumber } from "@/i18n/format";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";

export type InstalledStatusKind = "blocked" | "error" | "instance" | "workspace" | "off" | "receiving" | "none" | "theme-active" | "theme-inactive";

export interface InstalledStatus {
  kind: InstalledStatusKind;
  /** i18n key inside `integrations`. */
  key: string;
  values?: Record<string, string>;
}

/**
 * The single line the Installed list shows for an extension, with where the
 * state comes from: the workspace's own connection, or the instance level.
 * Order matters: a catalog block beats everything, a failing health check beats
 * "on".
 */
export function installedStatus(e: CatalogEntry): InstalledStatus {
  // a theme has no connection: it is the workspace's theme or it is not. That stays visible when the
  // catalog blocks it (the row shows the block on its own line), else an active theme would look absent.
  if (e.kind === "theme" && e.themeActive !== undefined) {
    return e.themeActive ? { kind: "theme-active", key: "installed.status.themeActive" } : { kind: "theme-inactive", key: "installed.status.themeInactive" };
  }
  if (e.blocked) {
    return { kind: "blocked", key: "installed.status.blocked", values: { reason: e.blockedReason || "-" } };
  }
  const s = e.status;
  if (s?.enabled && s.health && !s.health.ok) {
    return { kind: "error", key: "installed.status.error", values: { message: s.last_error || s.health.message || "-" } };
  }
  if (s?.enabled && s.source === "instance") return { kind: "instance", key: "installed.status.instance" };
  if (s?.enabled) return { kind: "workspace", key: "installed.status.workspace" };
  if (e.connected || s?.source === "workspace") return { kind: "off", key: "installed.status.off" };
  // Links keep arriving (instance-level webhook) although this workspace has no connection row.
  if (e.activity && e.activity.links > 0) {
    return {
      kind: "receiving", key: "installed.status.receiving",
      values: { count: formatNumber(e.activity.links), last: e.activity.last_event_at ? formatDateTime(e.activity.last_event_at) : "-" },
    };
  }
  return { kind: "none", key: "installed.status.none" };
}

/** The workspace-level settings schema of an extension, or null when it has none. */
export function workspaceSettingsSchema(e: CatalogEntry): Record<string, unknown> | null {
  if (e.manifest.levels && e.manifest.levels.workspace === false) return null;
  const s = e.manifest.settings?.workspace;
  return s && typeof s === "object" ? (s as Record<string, unknown>) : null;
}
