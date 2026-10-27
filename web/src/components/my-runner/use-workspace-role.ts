import { useEffect, useState } from "react";
import { api } from "@/lib/api";

/**
 * The signed-in user's role in workspace `wsId`, or null while unknown. The
 * shell does not load the role and the shared store keeps whichever response
 * landed last, so the Runner cards ask for their own workspace's role and keep
 * it keyed by workspace: a late answer for another workspace never unlocks this one.
 */
export function useWorkspaceRole(wsId: string | undefined): string | null {
  const [role, setRole] = useState<{ ws: string; role: string | null } | null>(null);
  useEffect(() => {
    if (!wsId) return;
    let live = true;
    setRole(null);
    api<{ role: string }>(`/api/v1/workspaces/${wsId}/members/me`)
      .then((me) => me?.role ?? null, () => null)
      .then((r) => { if (live) setRole({ ws: wsId, role: r }); });
    return () => { live = false; };
  }, [wsId]);
  return role && role.ws === wsId ? role.role : null;
}

export const isWorkspaceAdmin = (role: string | null) => role === "owner" || role === "admin";
