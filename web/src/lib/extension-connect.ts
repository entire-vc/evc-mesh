import { api } from "@/lib/api";
import type { Values } from "@/components/extensions/schema-form";

/** `POST /workspaces/{ws}/extensions/{id}/connect` (W1.11): 201, or 400 `details.consent` / `details.settings`. */
export function connectWorkspaceExtension(workspaceId: string, extensionId: string, settings: Values, capabilities: string[]) {
  return api(`/api/v1/workspaces/${workspaceId}/extensions/${extensionId}/connect`, {
    method: "POST",
    body: { settings, consent: { capabilities } },
  });
}

export interface ExtensionTestResult {
  ok: boolean;
  message: string;
}

/** `POST /workspaces/{ws}/extensions/{id}/test` — a fresh health check, never the 60 s cache. */
export function testWorkspaceExtension(workspaceId: string, extensionId: string) {
  return api<ExtensionTestResult>(`/api/v1/workspaces/${workspaceId}/extensions/${extensionId}/test`, { method: "POST" });
}
