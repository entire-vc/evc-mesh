import { useCan } from "./permissions";
import type { CanProps } from "@entire-vc/mesh-theme-sdk";

export { useCan };

/** Renders children only when the user holds the permission. */
export function Can({ permission, resource, fallback = null, children }: CanProps) {
  return <>{useCan(permission, resource) ? children : fallback}</>;
}
