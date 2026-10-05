import { api } from "@/lib/api";
import type { PaginatedResponse } from "@/types";

export const projectSettingsTabs = ["general", "statuses", "custom-fields", "members",
  "workflow", "assignment", "recurring", "templates", "integrations"];

export function projectSettingsTab(search: URLSearchParams): string {
  const tab = search.get("tab");
  return tab && projectSettingsTabs.includes(tab) ? tab : "general";
}

/**
 * An artifact's permanent address: `/a/<id>`, its own page, not a query
 * parameter on the owning task. The task-scoped form (`/t/<task>?artifact=`)
 * only worked while the reader could list that task's artifacts — and made
 * the link say "task" when it meant "file".
 */
export function artifactHref(artifactId: string): string {
  return `/a/${encodeURIComponent(artifactId)}`;
}

export function scheduleHref(wsSlug: string, projectSlug: string, scheduleId: string): string {
  return `/w/${encodeURIComponent(wsSlug)}/p/${encodeURIComponent(projectSlug)}/settings?${new URLSearchParams({ tab: "recurring", schedule: scheduleId })}`;
}

/** A deep link must search subsequent pages before declaring its ID missing. */
export async function fetchLinkedEntities<T extends { id: string }>(path: string, targetId?: string | null): Promise<T[]> {
  const items: T[] = [];
  let page = 1;
  for (;;) {
    const data = await api<PaginatedResponse<T>>(path, { params: { page } });
    items.push(...(data.items ?? []));
    if (!targetId || items.some(item => item.id === targetId) || !data.has_more) return items;
    page++;
  }
}
