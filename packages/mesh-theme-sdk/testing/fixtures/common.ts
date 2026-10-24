import type { FieldDescriptor, StatusData, TaskData } from "../../src/index";

export const NOW = "2026-01-01T00:00:00Z";

/** A task with every field set to its empty value (CT-common-02 relies on this being the baseline). */
export function task(id: string, over: Partial<TaskData> = {}): TaskData {
  return {
    id, shortId: id.slice(0, 8), projectId: "p1", title: `Task ${id}`, hasDescription: false, statusId: "s1",
    statusCategory: "todo", priority: "none", assignee: null, reviewer: null, checkedOutBy: null, labels: [],
    dueDate: null, overdue: false, estimatedHours: null, customFields: {},
    counts: { subtasks: 0, artifacts: 0, vcsLinks: 0 }, vcsProviders: [], vcsOwnedProviders: [],
    pipelineStatus: null, gate: null, runtimeState: null, delegationLevel: null, updatedAt: NOW,
    vcsProvidersKnown: true, checkout: null, recurring: null, falseOpen: null, ...over,
  };
}

export function status(id: string, over: Partial<StatusData> = {}): StatusData {
  return { id, name: id, slug: id, color: "#888", category: "todo", position: 0, ...over };
}

export const FIELD = {
  priority: { key: "priority", label: "Priority", kind: "badge", source: "builtin" },
  due_date: { key: "due_date", label: "Due date", kind: "date", source: "builtin" },
  assignee: { key: "assignee", label: "Assignee", kind: "person", source: "builtin" },
  labels: { key: "labels", label: "Labels", kind: "multiselect", source: "builtin" },
  sprint: { key: "cf:sprint", label: "Sprint", kind: "number", source: "custom", projectId: "p1" },
} as const satisfies Record<string, FieldDescriptor>;

export const fields = (): FieldDescriptor[] => [FIELD.priority, FIELD.due_date, FIELD.assignee, FIELD.labels, FIELD.sprint];
