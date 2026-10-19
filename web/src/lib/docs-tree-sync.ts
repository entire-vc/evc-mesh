/**
 * docs-tree-sync.ts — the server copy of which Docs tree branches a person has
 * open, so the tree looks the same on every device they use.
 *
 * Backed by GET/PUT /api/v1/me/ui-state/docs.tree:<project_id> (user_ui_state). The
 * server row is shared state, the localStorage copy in doc-tree.tsx is the
 * instant cache; this module only moves ids across the wire and never throws on
 * a network problem — a tree that cannot reach the server still works locally.
 */

import { api, ApiRequestError } from "@/lib/api";

interface UiStateResponse {
  state?: { expanded?: unknown } | null;
  updated_at?: string;
}

export type ServerExpanded =
  | { kind: "ok"; expanded: string[] }
  | { kind: "none" } // reachable, nothing stored yet
  | { kind: "error" }; // unreachable or refused: do not overwrite anything

const scopeFor = (projectId: string) =>
  encodeURIComponent(`docs.tree:${projectId}`);

export async function loadServerExpanded(
  projectId: string,
  signal?: AbortSignal,
): Promise<ServerExpanded> {
  try {
    const res = await api<UiStateResponse>(`/api/v1/me/ui-state/${scopeFor(projectId)}`, {
      ...(signal ? { signal } : {}),
    });
    const raw = res?.state?.expanded;
    if (!Array.isArray(raw)) return { kind: "none" };
    return {
      kind: "ok",
      expanded: raw.filter((v): v is string => typeof v === "string"),
    };
  } catch (err) {
    if (err instanceof ApiRequestError && err.status === 404) return { kind: "none" };
    return { kind: "error" };
  }
}

/** True when the server accepted the write. Last write wins on the server. */
export async function saveServerExpanded(
  projectId: string,
  expanded: string[],
): Promise<boolean> {
  try {
    await api(`/api/v1/me/ui-state/${scopeFor(projectId)}`, {
      method: "PUT",
      body: { state: { expanded } },
    });
    return true;
  } catch {
    return false;
  }
}
