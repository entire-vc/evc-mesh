/**
 * The one place that turns manifest `capabilities` into human wording. The
 * extension page (Permissions tab) and the connect wizard (W2.4) both read it,
 * so a right is never described two ways. Wording lives in the `integrations`
 * i18n namespace under `capabilities.*`; this file only maps keys.
 */

export interface ManifestCapabilities {
  api?: string[];
  events?: string[];
  webhooks_in?: string[];
  network?: { allowed_domains?: string[]; reasoning?: string };
  ui?: Array<string | { slot?: string }>;
  secrets?: string[];
}

export type CapabilityGroup = "api" | "events" | "webhooks" | "ui" | "secrets";

export interface CapabilityLine {
  group: CapabilityGroup;
  /** i18n key inside `integrations`, e.g. `capabilities.api.tasks:read`. */
  key: string;
  /** Interpolation values for the key (event name, hook name, slot). */
  values?: Record<string, string>;
  /** The raw manifest token, kept for tooltips and tests. */
  raw: string;
}

const API_KEYS = new Set([
  "tasks", "comments", "artifacts", "docs", "projects", "workspaces",
  "members", "agents", "agent_keys", "vcs_links", "events", "secrets",
]);

function slotName(slot: string | { slot?: string }): string {
  return typeof slot === "string" ? slot : (slot.slot ?? "");
}

/** Flat, ordered list of what the extension may do (domains are listed separately). */
export function describeCapabilities(caps: ManifestCapabilities | undefined | null): CapabilityLine[] {
  if (!caps) return [];
  const out: CapabilityLine[] = [];
  for (const raw of caps.api ?? []) {
    const [res = "", mode] = raw.split(":");
    if (API_KEYS.has(res) && (mode === "read" || mode === "write")) {
      out.push({ group: "api", key: `capabilities.api.${res}.${mode}`, raw });
    } else {
      out.push({ group: "api", key: "capabilities.api.unknown", values: { name: raw }, raw });
    }
  }
  for (const raw of caps.events ?? []) {
    out.push({ group: "events", key: "capabilities.events.listen", values: { event: raw }, raw });
  }
  for (const raw of caps.webhooks_in ?? []) {
    out.push({ group: "webhooks", key: "capabilities.webhooks.receive", values: { name: raw }, raw });
  }
  for (const s of caps.ui ?? []) {
    const slot = slotName(s);
    if (slot) out.push({ group: "ui", key: `capabilities.ui.${slot.replace(/\./g, "_")}`, values: { slot }, raw: slot });
  }
  for (const raw of caps.secrets ?? []) {
    out.push({ group: "secrets", key: "capabilities.secrets.store", values: { name: raw }, raw });
  }
  return out;
}

export function allowedDomains(caps: ManifestCapabilities | undefined | null): string[] {
  return caps?.network?.allowed_domains ?? [];
}
