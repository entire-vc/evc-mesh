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
