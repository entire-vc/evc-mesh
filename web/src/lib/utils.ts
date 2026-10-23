import { parseISO } from "date-fns";
import * as i18nFormat from "@/i18n/format";
import type { Priority, DelegationLevel, StatusCategory, Task, CreateTaskRequest } from "@/types";

export function formatDate(dateString: string): string {
  return i18nFormat.formatDate(dateString);
}

export function formatDateTime(dateString: string): string {
  return i18nFormat.formatDateTime(dateString);
}

export function formatRelative(dateString: string): string {
  return i18nFormat.formatRelativeTime(dateString);
}

// Cyrillic -> Latin transliteration so slugify() doesn't collapse Cyrillic
// names (e.g. "витрина") to an empty string.
const CYRILLIC_TRANSLIT: Record<string, string> = {
  а: "a", б: "b", в: "v", г: "g", д: "d", е: "e", ё: "e", ж: "zh",
  з: "z", и: "i", й: "y", к: "k", л: "l", м: "m", н: "n", о: "o",
  п: "p", р: "r", с: "s", т: "t", у: "u", ф: "f", х: "h", ц: "ts",
  ч: "ch", ш: "sh", щ: "sch", ъ: "", ы: "y", ь: "", э: "e", ю: "yu",
  я: "ya",
};

function transliterate(text: string): string {
  return text.replace(/[\u0400-\u04FF]/g, (ch) => CYRILLIC_TRANSLIT[ch] ?? "");
}

export function slugify(text: string): string {
  return transliterate(text.toLowerCase())
    .replace(/[^\w\s-]/g, "")
    .replace(/[\s_]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

// Custom field slugs are stored as snake_case (backend constraint:
// ^[a-z0-9_]{1,100}$, no hyphens) — unlike workspace/project slugs.
export function slugifyFieldKey(text: string): string {
  return transliterate(text.toLowerCase())
    .replace(/[^\w\s_]/g, "")
    .replace(/[\s-]+/g, "_")
    .replace(/^_+|_+$/g, "");
}

export function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`;
}

const pad2 = (n: number) => String(n).padStart(2, "0");

// Local-time "yyyy-MM-dd" / "HH:mm". Hand-rolled: date-fns format() pulls ~2 KB gz of
// token parsing into the start chunk for these two fixed patterns (perf-bundle budget).
export function localDateKey(d: Date): string {
  return `${String(d.getFullYear()).padStart(4, "0")}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

export function localClock(d: Date): string {
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// Convert ISO/RFC3339 string to datetime-local input format (YYYY-MM-DDTHH:mm)
export function toDateTimeLocal(dateString: string): string {
  const date = parseISO(dateString);
  return `${localDateKey(date)}T${localClock(date)}`;
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
  urgent: { label: "Urgent", color: "text-priority-urgent" },
  high: { label: "High", color: "text-priority-high" },
  medium: { label: "Medium", color: "text-priority-medium" },
  low: { label: "Low", color: "text-priority-low" },
  none: { label: "None", color: "text-muted-foreground" },
};

export const delegationLevelConfig: Record<
  DelegationLevel,
  { label: string; description: string; color: string }
> = {
  review: {
    label: "Review required",
    description: "Agent prepares artifact, waits for approval before executing.",
    color: "text-tone-purple-500",
  },
  auto: {
    label: "Auto-execute",
    description: "Agent completes task autonomously, notifies only.",
    color: "text-tone-success-500",
  },
  supervised: {
    label: "Supervised",
    description: "Agent proposes a plan; human triggers execution.",
    color: "text-tone-info-500",
  },
};

export const statusCategoryConfig: Record<
  StatusCategory,
  { label: string; color: string }
> = {
  backlog: { label: "Backlog", color: "bg-status-backlog" },
  triage: { label: "Triage", color: "bg-status-triage" },
  todo: { label: "To Do", color: "bg-status-todo" },
  in_progress: { label: "In Progress", color: "bg-status-in-progress" },
  review: { label: "Review", color: "bg-status-review" },
  done: { label: "Done", color: "bg-status-done" },
  cancelled: { label: "Cancelled", color: "bg-status-cancelled" },
};
