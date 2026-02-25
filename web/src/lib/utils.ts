import { format, formatDistanceToNow, parseISO } from "date-fns";
import type { Priority, StatusCategory, Task, CreateTaskRequest } from "@/types";

export function formatDate(dateString: string): string {
  return format(parseISO(dateString), "MMM d, yyyy");
}

export function formatDateTime(dateString: string): string {
  return format(parseISO(dateString), "MMM d, yyyy HH:mm");
}

export function formatRelative(dateString: string): string {
  return formatDistanceToNow(parseISO(dateString), { addSuffix: true });
}

export function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^\w\s-]/g, "")
    .replace(/[\s_]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

export function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`;
}

// Convert ISO/RFC3339 string to datetime-local input format (YYYY-MM-DDTHH:mm)
export function toDateTimeLocal(dateString: string): string {
  const date = parseISO(dateString);
  return format(date, "yyyy-MM-dd'T'HH:mm");
}

// Convert datetime-local input value to RFC3339 for backend
export function fromDateTimeLocal(dtLocal: string): string {
  return new Date(dtLocal).toISOString();
}

// Build a CreateTaskRequest that duplicates an existing task
export function buildDuplicateRequest(task: Task): CreateTaskRequest {
  return {
    title: `${task.title} (copy)`,
    description: task.description || undefined,
    priority: task.priority,
    status_id: task.status_id,
    labels: task.labels?.length ? [...task.labels] : undefined,
    assignee_id: task.assignee_id ?? undefined,
    assignee_type:
      task.assignee_type !== "unassigned" ? task.assignee_type : undefined,
    custom_fields: task.custom_fields ?? undefined,
    due_date: task.due_date ?? undefined,
    estimated_hours: task.estimated_hours ?? undefined,
  };
}

export const priorityConfig: Record<
  Priority,
  { label: string; color: string }
> = {
  urgent: { label: "Urgent", color: "text-red-600" },
  high: { label: "High", color: "text-orange-500" },
  medium: { label: "Medium", color: "text-yellow-500" },
  low: { label: "Low", color: "text-blue-500" },
  none: { label: "None", color: "text-muted-foreground" },
};

export const statusCategoryConfig: Record<
  StatusCategory,
  { label: string; color: string }
> = {
  backlog: { label: "Backlog", color: "bg-gray-400" },
  triage: { label: "Triage", color: "bg-amber-400" },
  todo: { label: "To Do", color: "bg-blue-400" },
  in_progress: { label: "In Progress", color: "bg-yellow-400" },
  review: { label: "Review", color: "bg-purple-400" },
  done: { label: "Done", color: "bg-green-400" },
  cancelled: { label: "Cancelled", color: "bg-red-400" },
};
