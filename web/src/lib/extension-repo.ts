import { api } from "@/lib/api";
import type { ManifestCapabilities } from "@/lib/extension-capabilities";

/**
 * Install from a repository (BRAT mode, W2.9) and the Updates tab. Every route is
 * for the instance administrator. The access token of a private repository is
 * only ever sent, never returned.
 */

export interface RepoRef {
  name: string;
  sha: string;
}

export interface RepoRefs {
  tags: RepoRef[];
  branches: RepoRef[];
}

export interface RepoPreview {
  id: string;
  name: string;
  version: string;
  publisher: string;
  description: string;
  mesh_api: string;
  compatible: boolean;
  capabilities: string[];
  manifest: { capabilities?: ManifestCapabilities };
  ref: string;
  commit_sha: string;
  trust_level: string;
  warning: string;
}

export interface RepoRequest {
  repo_url: string;
  ref?: string;
  auth_token?: string;
}

export interface RepoUpdate {
  extension_id: string;
  name: string;
  repo_url: string;
  current_version: string;
  current_ref: string;
  current_commit: string;
  latest_version?: string;
  latest_ref?: string;
  latest_commit?: string;
  update_available: boolean;
  new_capabilities: string[];
  needs_consent: boolean;
  pending: boolean;
  can_rollback: boolean;
  previous_version?: string;
  error?: string;
}

export type UpdateOutcome = "updated" | "up_to_date" | "pending_consent";

export interface UpdateResult {
  extension_id: string;
  outcome?: UpdateOutcome;
  version?: string;
  commit_sha?: string;
  new_capabilities: string[];
  error?: string;
}

export const listRepoRefs = (body: RepoRequest) => api<RepoRefs>("/api/v1/extensions/repo/refs", { method: "POST", body });
export const previewRepoExtension = (body: RepoRequest) =>
  api<RepoPreview>("/api/v1/extensions/repo/preview", { method: "POST", body });
const installExtension = (body: { source: "repo" | "catalog" } & Partial<RepoRequest> & { id?: string }) =>
  api<{ extension_id: string; version: string }>("/api/v1/extensions/install", { method: "POST", body });
export const installRepoExtension = (body: RepoRequest) => installExtension({ source: "repo", ...body });

export const listRepoUpdates = () => api<{ updates: RepoUpdate[] }>("/api/v1/extensions/updates");
export const updateRepoExtension = (id: string, consentCommitSha?: string) =>
  api<UpdateResult>(`/api/v1/extensions/${id}/update`, { method: "POST", body: consentCommitSha ? { consent_commit_sha: consentCommitSha } : {} });
export const updateAllRepoExtensions = () => api<{ results: UpdateResult[] }>("/api/v1/extensions/update-all", { method: "POST", body: {} });
export const rollbackRepoExtension = (id: string) => api(`/api/v1/extensions/${id}/rollback`, { method: "POST", body: {} });

export const shortSha = (sha: string | undefined) => (sha ?? "").slice(0, 8);

/** Install a Spark-catalog extension at its latest compatible version (instance admin only). */
export const installCatalogExtension = (id: string) => installExtension({ source: "catalog", id });

export interface CatalogCard {
  readme?: string;
  description?: string;
  versions?: { version: string; mesh_api?: string; capabilities_added?: string[]; capabilities_removed?: string[] }[];
  checks?: { result: "passed" | "warnings" | "failed" };
}

export const getCatalogCard = (id: string) => api<CatalogCard>(`/api/v1/extensions/${encodeURIComponent(id)}/catalog-card`);
