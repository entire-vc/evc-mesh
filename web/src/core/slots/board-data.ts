import { parseISO } from "date-fns";
import type { CustomFieldDefinition, Task, TaskStatus, StatusCategory, VCSProvider } from "@/types";
import { readableRuntimeState } from "@/lib/runtime-state";
import type { ActorRef, BoardColumnData, FieldDescriptor, StatusData, TaskData } from "./types";

const DONE_CATEGORIES = new Set<StatusCategory>(["done", "cancelled"]);

/** Today's card, as a field list: the rows and their order `mesh-default` draws without any setting. */
export const DEFAULT_CARD_FIELDS: readonly string[] = [
  "runtime",
  "labels",
  "assignee",
  "gate",
  "reviewer",
  "priority",
  "delegation",
  "due_date",
  "estimate",
  "subtasks",
  "artifacts",
];

/** Built-in card fields; labels are i18n keys of the `board` namespace (the core translates). */
export const BUILTIN_FIELDS: readonly { key: string; kind: FieldDescriptor["kind"]; labelKey: string }[] = [
  { key: "labels", kind: "multiselect", labelKey: "fields.labels" },
  { key: "runtime", kind: "badge", labelKey: "fields.runtime" },
  { key: "assignee", kind: "person", labelKey: "fields.assignee" },
  { key: "gate", kind: "flag", labelKey: "fields.gate" },
  { key: "reviewer", kind: "person", labelKey: "fields.reviewer" },
  { key: "priority", kind: "select", labelKey: "fields.priority" },
  { key: "delegation", kind: "badge", labelKey: "fields.delegation" },
  { key: "artifacts", kind: "number", labelKey: "fields.artifacts" },
  { key: "due_date", kind: "date", labelKey: "fields.dueDate" },
  { key: "subtasks", kind: "number", labelKey: "fields.subtasks" },
  { key: "estimate", kind: "number", labelKey: "fields.estimate" },
];

/** Theme field-picker keys that name a task column rather than a board card field; same i18n namespace as BUILTIN_FIELDS. */
export const PICKER_FIELD_LABEL_KEYS: Readonly<Record<string, string>> = {
  status: "fields.status",
  estimated_hours: "fields.estimate",
  created_at: "fields.created",
};

const CUSTOM_KIND: Record<string, FieldDescriptor["kind"]> = {
  number: "number",
  date: "date",
  select: "select",
  multiselect: "multiselect",
  checkbox: "flag",
};

/** Built-in fields plus the custom fields of the open project (custom fields belong to a project). */
export function buildFieldCatalog(
  customDefs: readonly CustomFieldDefinition[],
  translate: (key: string) => string,
): FieldDescriptor[] {
  const builtin = BUILTIN_FIELDS.map<FieldDescriptor>((f) => ({
    key: f.key,
    label: translate(f.labelKey),
    kind: f.kind,
    source: "builtin",
  }));
  const custom = [...customDefs]
    .sort((a, b) => a.position - b.position)
    .map<FieldDescriptor>((d) => ({
      key: `cf:${d.slug}`,
      label: d.name,
      kind: CUSTOM_KIND[d.field_type] ?? "text",
      source: "custom",
      projectId: d.project_id,
    }));
  return [...builtin, ...custom];
}

/**
 * The chosen fields in the chosen order. Unknown keys (a custom field of another
 * project, a field this version does not have) are skipped silently.
 */
export function resolveCardFields(catalog: readonly FieldDescriptor[], keys: readonly string[] | undefined): FieldDescriptor[] {
  const byKey = new Map(catalog.map((f) => [f.key, f]));
  const out: FieldDescriptor[] = [];
  for (const k of keys ?? DEFAULT_CARD_FIELDS) {
    const f = byKey.get(k);
    if (f) out.push(f);
  }
  return out;
}

/** The default selection as descriptors (no custom fields), for views drawn outside a board. */
export const DEFAULT_FIELD_DESCRIPTORS: FieldDescriptor[] = resolveCardFields(
  buildFieldCatalog([], (k) => k),
  undefined,
);

const statusCache = new WeakMap<TaskStatus, StatusData>();
export function toStatusData(s: TaskStatus): StatusData {
  let d = statusCache.get(s);
  if (!d) {
    d = { id: s.id, name: s.name, slug: s.slug, color: s.color, category: s.category, position: s.position };
    statusCache.set(s, d);
  }
  return d;
}

type TagOf = ReadonlyMap<string, string | null | undefined>;

const actor = (
  id: string | null | undefined,
  type: string | null | undefined,
  name: string | null | undefined,
  tags?: TagOf,
): ActorRef | null => {
  if (!(id && (type === "user" || type === "agent"))) return null;
  const ref: ActorRef = { id, type, name: name ?? "" };
  if (type === "agent" && tags?.has(id)) ref.shortTag = tags.get(id);
  return ref;
};

interface Cached {
  category: StatusCategory | undefined;
  holder: string | undefined;
  minute: number;
  tags: TagOf | undefined;
  owned: readonly VCSProvider[] | undefined;
  data: TaskData;
}
// A task the store did not touch keeps its object identity, so its TaskData must keep
// its identity too: the card memo compares it by reference.
const cache = new WeakMap<Task, Cached>();

/**
 * REST task -> the contract's TaskData. `statusCategory` is the category of the column
 * the card is drawn in (it decides `overdue`); `holderName` resolves the checkout holder.
 */
export function toTaskData(task: Task, statusCategory?: StatusCategory, holderName?: string, tags?: TagOf, ownedVcs?: readonly VCSProvider[]): TaskData {
  const minute = Math.floor(Date.now() / 60_000);
  const hit = cache.get(task);
  if (hit && hit.category === statusCategory && hit.holder === holderName && hit.tags === tags && hit.owned === ownedVcs && hit.minute === minute) return hit.data;

  const now = Date.now();
  const overdue =
    task.due_date != null &&
    !(statusCategory && DONE_CATEGORIES.has(statusCategory)) &&
    parseISO(task.due_date).getTime() < now;
  const expiry = task.checked_out_by && task.checkout_expires ? task.checkout_expires : null;
  const holderIsSomeoneElse = Boolean(holderName) && task.checked_out_by !== task.assignee_id;

  const data: TaskData = {
    id: task.id,
    shortId: task.id.slice(0, 8),
    projectId: task.project_id,
    title: task.title,
    hasDescription: task.has_description ?? Boolean(task.description && task.description.trim().length > 0),
    statusId: task.status_id,
    statusCategory: statusCategory ?? "todo",
    priority: task.priority,
    assignee: actor(task.assignee_id, task.assignee_type, task.assignee_name, tags),
    reviewer: actor(task.reviewer_id, task.reviewer_type, task.reviewer_name, tags),
    checkedOutBy: holderIsSomeoneElse && task.checked_out_by
      ? actor(task.checked_out_by, "agent", holderName, tags)
      : null,
    labels: task.labels ?? [],
    dueDate: task.due_date,
    overdue,
    estimatedHours: task.estimated_hours,
    customFields: task.custom_fields ?? {},
    counts: {
      subtasks: task.subtask_count ?? 0,
      openSubtasks: task.open_subtask_count ?? 0,
      dependencies: task.dependency_count ?? 0,
      openDependencies: task.open_dependency_count ?? 0,
      artifacts: task.artifact_count ?? 0,
      vcsLinks: task.vcs_link_count ?? 0,
    },
    vcsProviders: (task.vcs_providers ?? []).filter((p): p is "github" | "gitlab" => p === "github" || p === "gitlab"),
    pipelineStatus: task.vcs_pipeline_status ?? null,
    gate: task.human_gate
      ? { class: task.human_gate_class === "soft" ? "soft" : "hard", reason: task.gate_reason ?? "" }
      : null,
    runtimeState: task.runtime_state ? readableRuntimeState(task) : null,
    delegationLevel: task.delegation_level ?? null,
    updatedAt: task.updated_at,
    vcsProvidersKnown: task.vcs_providers !== undefined,
    vcsOwnedProviders: ownedVcs ?? [],
    checkout: expiry ? { expiresAt: expiry, lapsed: parseISO(expiry).getTime() < now } : null,
    recurring: task.recurring_schedule_id ? { instanceNumber: task.recurring_instance_number ?? null } : null,
    falseOpen: task.false_open
      ? {
          allChildrenClosed: task.false_open.all_children_closed,
          onlyParkedChildrenLeft: task.false_open.only_parked_children_left,
          openChildrenCount: task.false_open.open_children_count,
          staleDays: task.false_open.stale_days,
        }
      : null,
  };
  cache.set(task, { category: statusCategory, holder: holderName, tags, owned: ownedVcs, minute, data });
  return data;
}

/** True when both arrays hold the same TaskData objects in the same order. */
function sameTasks(a: readonly TaskData[], b: readonly TaskData[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  return a.every((t, i) => t === b[i]);
}

function sameRecord(a?: Record<string, string>, b?: Record<string, string>): boolean {
  if (a === b) return true;
  const ka = Object.keys(a ?? {});
  if (ka.length !== Object.keys(b ?? {}).length) return false;
  return ka.every((k) => a?.[k] === b?.[k]);
}

function sameColumn(a: BoardColumnData, b: BoardColumnData): boolean {
  return (
    a.id === b.id &&
    a.kind === b.kind &&
    a.title === b.title &&
    a.color === b.color &&
    a.status === b.status &&
    sameTasks(a.tasks, b.tasks) &&
    a.totalCount === b.totalCount &&
    a.countLabel === b.countLabel &&
    a.wipLimit === b.wipLimit &&
    a.isClosed === b.isClosed &&
    a.hasMore === b.hasMore &&
    a.loadingMore === b.loadingMore &&
    a.closedSlice?.loaded === b.closedSlice?.loaded &&
    a.closedSlice?.total === b.closedSlice?.total &&
    a.closedSlice?.filtersActive === b.closedSlice?.filtersActive &&
    a.group?.priority === b.group?.priority &&
    a.group?.assignee?.id === b.group?.assignee?.id &&
    a.group?.assignee?.name === b.group?.assignee?.name &&
    a.group?.assignee?.type === b.group?.assignee?.type &&
    a.group?.assignee?.shortTag === b.group?.assignee?.shortTag &&
    sameRecord(a.gateStatusNameByTask, b.gateStatusNameByTask)
  );
}

/**
 * Column data is rebuilt from scratch whenever anything upstream changes, including a
 * single card's drag; the column memo compares it by reference. Reuse the previous
 * object of a column whose content did not change, so unrelated columns stay put.
 */
export function stabilizeColumns(
  prev: ReadonlyMap<string, BoardColumnData>,
  next: readonly BoardColumnData[],
): { columns: BoardColumnData[]; cache: Map<string, BoardColumnData> } {
  const cache = new Map<string, BoardColumnData>();
  const columns = next.map((col) => {
    const old = prev.get(col.id);
    const stable = old && sameColumn(old, col) ? old : col;
    cache.set(col.id, stable);
    return stable;
  });
  return { columns, cache };
}
