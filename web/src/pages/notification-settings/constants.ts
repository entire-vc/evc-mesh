// ---------------------------------------------------------------------------
// Event definitions shown in the settings UI
// ---------------------------------------------------------------------------

// `id` names the locale entry: notifications:settings.events.<id>.{label,description,pushDescription}
export interface EventConfig {
  key: string;
  id: string;
}

export const NOTIFICATION_EVENTS: EventConfig[] = [
  { key: "task.assigned", id: "taskAssigned" },
  { key: "task.status_changed", id: "statusChanged" },
  { key: "comment.created", id: "commentCreated" },
  { key: "task.mentioned", id: "taskMentioned" },
  { key: "document.mentioned", id: "documentMentioned" },
  { key: "task.blocking_triage", id: "blockingTriage" },
  { key: "task.reviewer_assigned", id: "reviewerAssigned" },
  { key: "task.ready_for_review", id: "readyForReview" },
];

export type TabId = "in-app" | "push" | "email" | "telegram";

// notification_preferences.config.scope; a missing value means DEFAULT_SCOPE.
export const SCOPES = ["all", "mine", "mentions"] as const;
export type NotificationScope = (typeof SCOPES)[number];
export const DEFAULT_SCOPE: NotificationScope = "mine";

export function readScope(config: Record<string, unknown> | undefined): NotificationScope {
  const v = config?.scope;
  return (SCOPES as readonly string[]).includes(v as string) ? (v as NotificationScope) : DEFAULT_SCOPE;
}
