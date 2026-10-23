import type { ReactNode } from "react";
import { useCan } from "./permissions";
import type { Permission, ResourceRef } from "./types";

export { useCan };

/** Renders children only when the user holds the permission. */
export function Can({
  permission,
  resource,
  fallback = null,
  children,
}: {
  permission: Permission;
  resource?: ResourceRef;
  fallback?: ReactNode;
  children: ReactNode;
}) {
  return <>{useCan(permission, resource) ? children : fallback}</>;
}
