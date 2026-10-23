import { useMemberStore } from "@/stores/member";
import type { WorkspaceRole } from "@/types";
import type { Permission, ResourceRef } from "./types";

const VIEWER_DENIED: ReadonlySet<Permission> = new Set([
  "task.move",
  "task.edit",
  "task.create",
  "doc.move",
  "doc.edit",
]);

/**
 * Role -> permission. An unknown role (not loaded yet, or the lookup failed)
 * allows, exactly as the UI behaved before permissions were a primitive: the
 * server stays the authority and refuses what the user may not do.
 */
export function roleCan(role: WorkspaceRole | null, permission: Permission): boolean {
  if (role === "viewer") return !VIEWER_DENIED.has(permission);
  return true;
}

export function useCan(permission: Permission, _resource?: ResourceRef): boolean {
  const role = useMemberStore((s) => s.myRole);
  return roleCan(role, permission);
}
