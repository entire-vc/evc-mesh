// Who may act on initiatives, by workspace role. Mirrors the routes in
// cmd/api/routes_planning.go: create, edit, link and unlink need
// PermCreateProject (owner, admin, member); delete needs PermDeleteProject
// (owner, admin). A viewer gets none. An unknown role (not loaded yet, or the
// lookup failed) shows no actions: a button the server would refuse with 403
// is worse than a button that appears a moment later.
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import type { WorkspaceRole } from "@/types";

export interface InitiativeRights {
  create: boolean;
  edit: boolean;
  link: boolean;
  delete: boolean;
}

export function initiativeRights(role: WorkspaceRole | null): InitiativeRights {
  const write = role === "owner" || role === "admin" || role === "member";
  const del = role === "owner" || role === "admin";
  return { create: write, edit: write, link: write, delete: del };
}

// The role counts only once it is settled for the open workspace: right after a
// switch the store still holds the previous workspace's role.
export function useInitiativeRights(): InitiativeRights {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const role = useMemberStore((s) => (s.myRoleLoaded && s.myRoleWorkspaceId === wsId ? s.myRole : null));
  return initiativeRights(role);
}
