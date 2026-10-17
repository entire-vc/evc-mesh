import { api } from "@/lib/api";
import { refreshSlotRegistryAfterChange } from "@/lib/extension-slots";
import type { FieldSchema, Values } from "@/components/extensions/schema-form";

/**
 * Instance administration of extensions (W2.8): allow-list, auto-update flag,
 * journal and the per-extension instance settings. Every route is for the
 * instance administrator; anyone else gets a 403 which the page shows as "no access".
 */

export interface InstanceOverviewItem {
  id: string;
  name: string;
  kind: string;
  trust_level: string;
  allowed: boolean;
  allow_explicit: boolean;
  has_instance_form: boolean;
  enabled: boolean;
  settings_source: "none" | "env" | "admin";
  settings_updated_at?: string;
}

export interface InstanceOverview {
  auto_update: boolean;
  items: InstanceOverviewItem[];
}

export interface InstanceJournalEntry {
  id: string;
  actor_id?: string;
  actor_name?: string;
  extension_id: string;
  action: "settings_changed" | "allow_changed" | "auto_update_changed" | string;
  details: Record<string, unknown>;
  created_at: string;
}

export interface InstanceSettingsDto {
  extension_id: string;
  settings: Values;
  enabled: boolean;
  source: "none" | "env" | "admin";
  updated_at?: string;
}

export interface InstanceExtensionManifest {
  settings?: { instance?: FieldSchema };
  [k: string]: unknown;
}

export interface InstanceExtensionSummary {
  id: string;
  name: string;
  manifest?: InstanceExtensionManifest;
}

export const getInstanceOverview = () => api<InstanceOverview>("/api/v1/extensions/instance/overview");
export const getInstanceJournal = (limit = 50) =>
  api<{ entries: InstanceJournalEntry[] }>("/api/v1/extensions/instance/journal", { params: { limit } });
export const getInstanceSettings = (id: string) => api<InstanceSettingsDto>(`/api/v1/extensions/${id}/instance-settings`);
export const listExtensionSummaries = () => api<{ extensions: InstanceExtensionSummary[] }>("/api/v1/extensions");

export const setExtensionAllowed = (id: string, allowed: boolean) =>
  api(`/api/v1/extensions/${id}/allowed`, { method: "PUT", body: { allowed } }).then((res) => {
    refreshSlotRegistryAfterChange();
    return res;
  });
export const setInstanceAutoUpdate = (autoUpdate: boolean) =>
  api("/api/v1/extensions/instance/auto-update", { method: "PUT", body: { auto_update: autoUpdate } });

/**
 * `DD.MM` of the moment the env values were copied into the instance settings;
 * empty when the timestamp is missing or unparsable (the badge then has no date).
 */
export function envMigratedDate(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getUTCDate())}.${pad(d.getUTCMonth() + 1)}`;
}

/** Trust levels that are on the allow-list by default; the rest need an explicit allow. */
export function allowedByDefault(trust: string): boolean {
  return trust === "" || trust === "builtin" || trust === "official" || trust === "verified";
}
