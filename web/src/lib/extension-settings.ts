import { api } from "@/lib/api";
import { refreshSlotRegistryAfterChange } from "@/lib/extension-slots";
import type { Values } from "@/components/extensions/schema-form";

/**
 * Thin save layer for extension settings. Paths follow docs/extensions/api.md
 * (W1.11 contract); the server validates against the manifest and answers a
 * failed field with `details`, which SchemaForm shows line by line.
 */

/** The enabled set may have changed: slot contributors on the board and task view must follow without a reload. */
function afterSave<T>(res: T): T {
  refreshSlotRegistryAfterChange();
  return res;
}

/** `PATCH /workspaces/{ws}/extensions/{id}/settings` — merged over the current settings. */
export function saveWorkspaceExtensionSettings(workspaceId: string, extensionId: string, settings: Values) {
  return api(`/api/v1/workspaces/${workspaceId}/extensions/${extensionId}/settings`, {
    method: "PATCH",
    body: { settings },
  }).then(afterSave);
}

/** `PUT /extensions/{id}/instance-settings` (W1.3) — instance administrator only. */
export function saveInstanceExtensionSettings(extensionId: string, body: { enabled: boolean; settings: Values }) {
  return api(`/api/v1/extensions/${extensionId}/instance-settings`, { method: "PUT", body }).then(afterSave);
}

/** `PUT /projects/{p}/extensions/{id}` — created or replaced. */
export function saveProjectExtension(projectId: string, extensionId: string, body: { enabled: boolean; settings: Values }) {
  return api(`/api/v1/projects/${projectId}/extensions/${extensionId}`, { method: "PUT", body }).then(afterSave);
}

const wsBase = (workspaceId: string, extensionId: string) =>
  `/api/v1/workspaces/${workspaceId}/extensions/${extensionId}`;

/** `POST …/connect` — `consent.capabilities` must cover everything the manifest declares. */
export function connectWorkspaceExtension(workspaceId: string, extensionId: string, settings: Values, capabilities: string[]) {
  return api(`${wsBase(workspaceId, extensionId)}/connect`, {
    method: "POST",
    body: { settings, consent: { capabilities } },
  });
}

/**
 * `POST …/enable|disable`. Switching off an extension the instance governs
 * (`status.source === "instance"`) needs `confirm: true`; the server answers
 * 409 `confirmation_required` without it and changes nothing.
 */
export function setWorkspaceExtensionEnabled(workspaceId: string, extensionId: string, enabled: boolean, confirm = false) {
  return api(`${wsBase(workspaceId, extensionId)}/${enabled ? "enable" : "disable"}`, {
    method: "POST",
    body: enabled ? undefined : { confirm },
  }).then(afterSave);
}

export interface ExtensionTestResult { ok: boolean; message: string }

/** `POST …/test` — fresh health check. */
export function testWorkspaceExtension(workspaceId: string, extensionId: string) {
  return api<ExtensionTestResult>(`${wsBase(workspaceId, extensionId)}/test`, { method: "POST" });
}

/** `DELETE …?data=keep|delete` — removes the connection, optionally the extension's data. */
export function disconnectWorkspaceExtension(workspaceId: string, extensionId: string, data: "keep" | "delete") {
  return api(`${wsBase(workspaceId, extensionId)}?data=${data}`, { method: "DELETE" }).then(afterSave);
}

/** One binding of an extension to a project (`ProjectExtension`, docs/extensions/api.md). */
export interface ProjectBinding {
  extension_id: string;
  enabled: boolean;
  settings: Values;
  key_expires_at?: string;
  key_expiring_soon?: boolean;
  last_sync_checked_at?: string;
  last_sync_status?: string;
  last_sync_error?: string;
}

/** `GET /projects/{p}/extensions` — the bindings this project has. */
export async function listProjectBindings(projectId: string): Promise<ProjectBinding[]> {
  const res = await api<{ extensions: ProjectBinding[] | null }>(`/api/v1/projects/${projectId}/extensions`);
  return res.extensions ?? [];
}

/** `DELETE /projects/{p}/extensions/{id}` — unbind; the extension stays connected to the workspace. */
export function deleteProjectExtension(projectId: string, extensionId: string) {
  return api(`/api/v1/projects/${projectId}/extensions/${extensionId}`, { method: "DELETE" }).then(afterSave);
}

export interface ProjectAction { name: string; method: string; title?: string }

/** `GET /projects/{p}/extensions/{id}/actions` — what the extension can do for this project. */
export async function listProjectActions(projectId: string, extensionId: string): Promise<ProjectAction[]> {
  const res = await api<{ actions: ProjectAction[] | null }>(`/api/v1/projects/${projectId}/extensions/${extensionId}/actions`);
  return res.actions ?? [];
}

export interface CheckResult { ok: boolean; message: string; checked_at?: string }

/** `POST .../actions/check` — health of the connection the binding relies on; writes nothing. */
export function checkProjectExtension(projectId: string, extensionId: string) {
  return api<CheckResult>(`/api/v1/projects/${projectId}/extensions/${extensionId}/actions/check`, { method: "POST" });
}

/** `POST .../actions/{name}` — a provider action such as the Team Relay `mount`. */
export function runProjectAction(projectId: string, extensionId: string, name: string) {
  return api<unknown>(`/api/v1/projects/${projectId}/extensions/${extensionId}/actions/${name}`, { method: "POST" });
}

export interface ActionSearchDoc { id: string; title: string; path: string }

/** `GET .../actions/search?q=` — search the content a binding exposes (Team Relay share documents). */
export async function searchProjectAction(projectId: string, extensionId: string, q: string): Promise<ActionSearchDoc[]> {
  const res = await api<{ docs: ActionSearchDoc[] | null }>(
    `/api/v1/projects/${projectId}/extensions/${extensionId}/actions/search?q=${encodeURIComponent(q)}`,
  );
  return res.docs ?? [];
}
