import { useEffect, useState } from "react";
import { getChatProjectSettings } from "./api";

// Whether a project turned Chat with Agent on, per project, for the tab strip.
// One request per project per page load; the settings card and the chat page
// write what they learn so the tab follows a change without a reload.
const known = new Map<string, boolean>();
const listeners = new Set<() => void>();

export function setChatEnabled(projectId: string, enabled: boolean): void {
  if (known.get(projectId) === enabled) return;
  known.set(projectId, enabled);
  listeners.forEach((l) => l());
}

export function useChatEnabled(projectId: string | undefined): boolean {
  const [, bump] = useState(0);
  useEffect(() => {
    const l = () => bump((n) => n + 1);
    listeners.add(l);
    return () => void listeners.delete(l);
  }, []);
  useEffect(() => {
    if (!projectId || known.has(projectId)) return;
    // Optimistically false while in flight; a failed request is forgotten so the
    // next mount asks again instead of hiding the tab until a reload.
    known.set(projectId, false);
    getChatProjectSettings(projectId)
      .then((s) => setChatEnabled(projectId, s.enabled && s.agents.length > 0))
      .catch(() => void known.delete(projectId));
  }, [projectId]);
  return projectId ? (known.get(projectId) ?? false) : false;
}
