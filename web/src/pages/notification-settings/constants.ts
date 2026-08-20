// ---------------------------------------------------------------------------
// Event definitions shown in the settings UI
// ---------------------------------------------------------------------------

export interface EventConfig {
  key: string;
  label: string;
  description: string;
}

export const NOTIFICATION_EVENTS: EventConfig[] = [
  {
    key: "task.assigned",
    label: "Task assigned",
    description: "When a task is assigned to you or someone in your workspace",
  },
  {
    key: "task.status_changed",
    label: "Status changed",
    description: "When a task's status changes",
  },
  {
    key: "comment.created",
    label: "New comment",
    description: "When a comment is added to a task",
  },
  {
    key: "task.mentioned",
    label: "Mention",
    description: "When someone @mentions you in a comment",
  },
  {
    key: "document.mentioned",
    label: "Mention on a document",
    description: "When someone @mentions you in a comment on a document page",
  },
  {
    key: "task.blocking_triage",
    label: "Blocking triage",
    description: "When a task you're mentioned in is auto-moved to triage as a blocker",
  },
  {
    key: "task.reviewer_assigned",
    label: "Review requested",
    description: "When you're set as the reviewer on a task",
  },
  {
    key: "task.ready_for_review",
    label: "Ready for review",
    description: "When a task you're reviewing moves to a review status",
  },
];

export type TabId = "in-app" | "push" | "email" | "telegram";
