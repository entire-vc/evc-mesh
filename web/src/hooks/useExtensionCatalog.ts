import type { FieldSchema } from "@/components/extensions/schema-form";
import { useCallback, useEffect, useState } from "react";
import { api, ApiRequestError } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import type { ManifestCapabilities } from "@/lib/extension-capabilities";

export type ExtensionKind = "integration" | "plugin" | "theme";
export type TrustLevel = "official" | "verified" | "community" | "custom";

export interface ExtensionStatusDto {
  enabled: boolean;
  source: "workspace" | "instance" | "none";
  version?: string;
  health?: { ok: boolean; message: string };
  last_error?: string;
}

export interface ExtensionManifest {
  mesh_api?: string;
  icon?: string;
  readme?: string;
  repository?: string;
  homepage?: string;
  license?: string;
  screenshots?: string[];
  capabilities?: ManifestCapabilities;
  levels?: { instance?: boolean; workspace?: boolean; project?: boolean };
  /** Settings schemas per level; a level may be a `$ref` into `schemas`. */
  settings?: { instance?: FieldSchema; workspace?: FieldSchema; project?: FieldSchema };
  schemas?: Record<string, unknown>;
}

/** Links the workspace's own data holds for a VCS extension, connection row or not. */
export interface ExtensionActivityDto {
  links: number;
  last_event_at?: string;
}

export interface ConnectionDto {
  settings?: Record<string, unknown>;
  consented_capabilities?: string[];
}

/** One catalog row: instance summary merged with the workspace's own status. */
export interface CatalogEntry {
  id: string;
  name: string;
  version: string;
  kind: ExtensionKind;
  runtime: string;
  publisher: string;
  description: string;
  category: string;
  trust: TrustLevel;
  source: string;
  blocked: boolean;
  blockedReason: string;
  /** False only when the instance admin's list excludes it (field arrives with W2.8). */
  allowed: boolean;
  /** Present in this instance (built-in or installed); false for catalog-only entries. */
  installed: boolean;
  /** Workspace has a connection, or the instance level governs it. */
  connected: boolean;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  manifest: ExtensionManifest;
  status?: ExtensionStatusDto;
  connection?: ConnectionDto | null;
  activity?: ExtensionActivityDto;
  /** Catalog-supplied extras (absent for built-ins). */
  versions?: VersionEntry[];
  checks?: ChecksCard;
  /** Removing the connection can also delete the extension's data (`?data=delete`). */
  canPurgeData: boolean;
  /** Settings are written only through the extension's own API; generic connect/settings/remove refuse. */
  managedElsewhere: boolean;
}

export interface VersionEntry {
  version: string;
  released_at?: string;
  changes?: string;
  mesh_api?: string;
  capabilities_added?: string[];
  capabilities_removed?: string[];
}

export interface ChecksCard {
  result: "passed" | "warnings" | "failed";
  checked_at?: string;
  reviewer?: string;
}

export interface SummaryDto {
  id: string; name: string; version: string; kind: string; runtime: string;
  publisher: string; description: string; category: string; source: string;
  trust_level: string; blocked: boolean; blocked_reason: string;
  allowed?: boolean; created_at?: string; updated_at?: string;
  manifest?: ExtensionManifest;
  versions?: VersionEntry[]; checks?: ChecksCard;
}
export interface WorkspaceDto {
  id: string; name: string; version: string; kind: string; category: string;
  manifest?: ExtensionManifest & { runtime?: string; publisher?: string; description?: string };
  status?: ExtensionStatusDto; connection?: ConnectionDto | null; activity?: ExtensionActivityDto;
  can_purge_data?: boolean; settings_managed_elsewhere?: boolean;
}

export function trustOf(level: string, source: string): TrustLevel {
  if (level === "builtin" || level === "official") return "official";
  if (level === "verified") return "verified";
  if (level === "community") return "community";
  if (level === "custom" || source === "repo") return "custom";
  return "community";
}

/**
 * The workspace list is the base: it names every registered extension and its
 * status. The instance list (`GET /extensions`) adds source, trust, block and
 * timestamps where the extension has an installed row; a registered built-in
 * without one is still official. Catalog extras are extensions not installed here.
 */
export function mergeCatalog(
  summaries: SummaryDto[],
  workspace: WorkspaceDto[],
  catalogExtras: SummaryDto[] = [],
): CatalogEntry[] {
  const inst = new Map(summaries.map((s) => [s.id, s]));
  const rows: CatalogEntry[] = [];
  const row = (s: SummaryDto, w: WorkspaceDto | undefined, installed: boolean): CatalogEntry => {
    const status = w?.status;
    return {
      id: s.id, name: s.name, version: s.version,
      kind: s.kind === "plugin" || s.kind === "theme" ? s.kind : "integration",
      runtime: s.runtime, publisher: s.publisher, description: s.description,
      category: s.category, trust: trustOf(s.trust_level, s.source), source: s.source,
      blocked: s.blocked, blockedReason: s.blocked_reason,
      allowed: s.allowed ?? !s.blocked,
      installed,
      connected: Boolean(w?.connection) || status?.source === "instance",
      enabled: Boolean(status?.enabled),
      createdAt: s.created_at ?? "", updatedAt: s.updated_at ?? "",
      manifest: s.manifest ?? {}, status, connection: w?.connection, activity: w?.activity, versions: s.versions, checks: s.checks,
      canPurgeData: Boolean(w?.can_purge_data), managedElsewhere: Boolean(w?.settings_managed_elsewhere),
    };
  };
  for (const w of workspace) {
    const m = w.manifest ?? {};
    const s: SummaryDto = {
      id: w.id, name: w.name, version: w.version, kind: w.kind,
      runtime: m.runtime ?? "builtin", publisher: m.publisher ?? "", description: m.description ?? "",
      category: w.category, source: "builtin", trust_level: "builtin", blocked: false, blocked_reason: "",
      manifest: m,
      ...inst.get(w.id),
    };
    rows.push(row(s, w, true));
  }
  const seen = new Set(rows.map((r) => r.id));
  for (const s of [...summaries, ...catalogExtras]) {
    if (seen.has(s.id)) continue;
    seen.add(s.id);
    rows.push(row(s, undefined, inst.has(s.id)));
  }
  return rows;
}

export interface CatalogState {
  state: "loading" | "error" | "ready";
  entries: CatalogEntry[];
  /** The catalog source (Spark proxy) answered with an outage; built-ins still shown. */
  catalogUnavailable: boolean;
  /** Re-fetch behind the current screen (no skeleton). */
  refresh: () => void;
  reload: () => void;
}

/**
 * `GET /extensions` (installed + built-in) merged with
 * `GET /workspaces/{ws}/extensions` (status). The Spark-backed
 * `GET /extensions/catalog` joins in W4.1: a 404 means "not deployed yet" and is
 * silent; any other failure raises the "catalog unavailable" banner.
 */
export function useExtensionCatalog(): CatalogState {
  const { currentWorkspace } = useWorkspaceStore();
  const wsId = currentWorkspace?.id;
  const [data, setData] = useState<{ entries: CatalogEntry[]; unavailable: boolean } | null>(null);
  const [failed, setFailed] = useState(false);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    if (!wsId) return;
    let cancelled = false;
    setFailed(false);
    (async () => {
      try {
        const [list, mine] = await Promise.all([
          api<{ extensions: SummaryDto[] }>("/api/v1/extensions"),
          api<{ extensions: WorkspaceDto[] }>(`/api/v1/workspaces/${wsId}/extensions`),
        ]);
        let extras: SummaryDto[] = [];
        let unavailable = false;
        try {
          extras = (await api<{ extensions: SummaryDto[] }>("/api/v1/extensions/catalog")).extensions ?? [];
        } catch (e) {
          unavailable = !(e instanceof ApiRequestError && e.status === 404);
        }
        if (!cancelled) {
          setData({ entries: mergeCatalog(list.extensions ?? [], mine.extensions ?? [], extras), unavailable });
        }
      } catch {
        if (!cancelled) setFailed(true);
      }
    })();
    return () => { cancelled = true; };
  }, [wsId, nonce]);

  const reload = useCallback(() => { setData(null); setNonce((n) => n + 1); }, []);
  const refresh = useCallback(() => setNonce((n) => n + 1), []);
  if (failed) return { state: "error", entries: [], catalogUnavailable: false, refresh, reload };
  if (!data) return { state: "loading", entries: [], catalogUnavailable: false, refresh, reload };
  return { state: "ready", entries: data.entries, catalogUnavailable: data.unavailable, refresh, reload };
}
