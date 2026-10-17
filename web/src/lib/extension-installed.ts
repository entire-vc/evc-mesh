import type { CatalogEntry } from "@/hooks/useExtensionCatalog";

export type InstalledStatusKind = "blocked" | "error" | "instance" | "workspace" | "off" | "none";

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
  return { kind: "none", key: "installed.status.none" };
}

/** The workspace-level settings schema of an extension, or null when it has none. */
export function workspaceSettingsSchema(e: CatalogEntry): Record<string, unknown> | null {
  if (e.manifest.levels && e.manifest.levels.workspace === false) return null;
  const s = e.manifest.settings?.workspace;
  return s && typeof s === "object" ? (s as Record<string, unknown>) : null;
}
