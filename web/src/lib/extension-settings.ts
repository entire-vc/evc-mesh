import { api } from "@/lib/api";
import type { Values } from "@/components/extensions/schema-form";

/**
 * Thin save layer for extension settings. Paths follow docs/extensions/api.md
 * (W1.11 contract); the server validates against the manifest and answers a
 * failed field with `details`, which SchemaForm shows line by line.
 */

/** `PATCH /workspaces/{ws}/extensions/{id}/settings` — merged over the current settings. */
export function saveWorkspaceExtensionSettings(workspaceId: string, extensionId: string, settings: Values) {
  return api(`/api/v1/workspaces/${workspaceId}/extensions/${extensionId}/settings`, {
    method: "PATCH",
    body: { settings },
  });
}

/** `PUT /extensions/{id}/instance-settings` (W1.3) — instance administrator only. */
export function saveInstanceExtensionSettings(extensionId: string, body: { enabled: boolean; settings: Values }) {
  return api(`/api/v1/extensions/${extensionId}/instance-settings`, { method: "PUT", body });
}

/** `PUT /projects/{p}/extensions/{id}` — created or replaced. */
export function saveProjectExtension(projectId: string, extensionId: string, body: { enabled: boolean; settings: Values }) {
  return api(`/api/v1/projects/${projectId}/extensions/${extensionId}`, { method: "PUT", body });
}
