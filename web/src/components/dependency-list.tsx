import { useState, useEffect, useCallback } from "react";
import { ArrowRight, ArrowLeft, Link2, GitMerge, Plus, X, Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/cn";
import { useProjectStore } from "@/stores/project";
import type { DependencyType, TaskDependency, TaskDependencyList } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// DisplayGroup is direction-aware: the same dependency_type reads differently
// depending on which side of the edge the current task is on. A "blocks" edge
// this task CREATED means this task is blocked by the other one; the same
// edge viewed from the other task's Dependencies tab means it blocks this one.
type DisplayGroup = "blocked_by" | "blocks" | "relates_to" | "child_of" | "parent_of";

const GROUP_CONFIG: Record<
  DisplayGroup,
  { label: string; icon: typeof ArrowRight; badgeClass: string }
> = {
  blocked_by: {
    label: "Blocked by",
    icon: ArrowLeft,
    badgeClass: "bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300",
  },
  blocks: {
    label: "Blocks",
    icon: ArrowRight,
    badgeClass: "bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300",
  },
  relates_to: {
    label: "Relates to",
    icon: Link2,
    badgeClass: "bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300",
  },
  child_of: {
    label: "Child of",
    icon: GitMerge,
    badgeClass:
      "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300",
  },
  parent_of: {
    label: "Parent of",
    icon: GitMerge,
    badgeClass:
      "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300",
  },
};

const GROUP_ORDER: DisplayGroup[] = [
  "child_of",
  "parent_of",
  "blocked_by",
  "blocks",
  "relates_to",
];

// The 3 creatable dependency types. Creating one always produces an OUTGOING
// edge on the current task, so the label/hint here is written from that
// task's point of view — it's what the resulting edge will read as on this
// task's own Dependencies tab.
const CREATE_TYPE_CONFIG: Record<
  DependencyType,
  { label: string; hint: string }
> = {
  blocks: {
    label: "Blocked by",
    hint: "The task you enter must finish before this one can — it blocks this task.",
  },
  relates_to: {
    label: "Relates to",
    hint: "Just a reference. No ordering or hierarchy is implied.",
  },
  is_child_of: {
    label: "Child of",
    hint: "The task you enter becomes the PARENT of this task — this task becomes its subtask.",
  },
};

const CREATE_TYPE_ORDER: DependencyType[] = ["blocks", "relates_to", "is_child_of"];

function outgoingGroup(type: DependencyType): DisplayGroup {
  if (type === "is_child_of") return "child_of";
  if (type === "blocks") return "blocked_by";
  return "relates_to";
}

function incomingGroup(type: DependencyType): DisplayGroup {
  if (type === "is_child_of") return "parent_of";
  if (type === "blocks") return "blocks";
  return "relates_to";
}

interface DisplayRow {
  dep: TaskDependency;
  group: DisplayGroup;
  /** The task id whose route DELETE must target — the task that owns the edge. */
  ownerTaskId: string;
  /** The OTHER task in the edge, i.e. what this row is showing/linking to. */
  relatedTaskId: string;
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface DependencyListProps {
  taskId: string;
  className?: string;
  /** Called after a dependency is successfully added or removed. */
  onChanged?: () => void;
  /** Called when the user clicks a related task's name. */
  onOpenTask?: (taskId: string) => void;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function DependencyList({
  taskId,
  className,
  onChanged,
  onOpenTask,
}: DependencyListProps) {
  const { statuses } = useProjectStore();
  const [outgoing, setOutgoing] = useState<TaskDependency[]>([]);
  const [incoming, setIncoming] = useState<TaskDependency[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [form, setForm] = useState<{
    depends_on_task_id: string;
    dependency_type: DependencyType;
  }>({
    depends_on_task_id: "",
    dependency_type: "blocks",
  });

  // ---- Fetch ---------------------------------------------------------------

  const fetchDeps = useCallback(async () => {
    try {
      const data = await api<TaskDependencyList>(
        `/api/v1/tasks/${taskId}/dependencies`,
      );
      setOutgoing(Array.isArray(data?.outgoing) ? data.outgoing : []);
      setIncoming(Array.isArray(data?.incoming) ? data.incoming : []);
    } catch {
      // Non-fatal — show empty state
      setOutgoing([]);
      setIncoming([]);
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  useEffect(() => {
    setLoading(true);
    setOutgoing([]);
    setIncoming([]);
    void fetchDeps();
  }, [fetchDeps]);

  // ---- Actions -------------------------------------------------------------

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = form.depends_on_task_id.trim();
    if (!trimmed) {
      setError("Task ID is required.");
      return;
    }
    // Basic UUID validation
    const uuidRegex =
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    if (!uuidRegex.test(trimmed)) {
      setError("Please enter a valid task UUID (e.g. 550e8400-e29b-41d4-a716-446655440000).");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await api<TaskDependency>(`/api/v1/tasks/${taskId}/dependencies`, {
        method: "POST",
        body: {
          depends_on_task_id: trimmed,
          dependency_type: form.dependency_type,
        },
      });
      // Refetch rather than optimistically appending — the create response
      // has no related_task_title/status yet, and for is_child_of this task's
      // own Subtasks tab elsewhere on the page also needs to know to refresh.
      await fetchDeps();
      onChanged?.();
      setShowForm(false);
      setForm({ depends_on_task_id: "", dependency_type: "blocks" });
    } catch (err: unknown) {
      setError(
        apiErrorMessage(err, "Failed to add dependency."),
      );
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (row: DisplayRow) => {
    setDeletingId(row.dep.id);
    try {
      // An incoming edge is owned by the OTHER task (it's the one that has
      // task_id = that task, depends_on_task_id = this one), so the delete
      // route has to target that task, not the one this component was mounted
      // for.
      await api(`/api/v1/tasks/${row.ownerTaskId}/dependencies/${row.dep.id}`, {
        method: "DELETE",
      });
      await fetchDeps();
      onChanged?.();
    } catch {
      // Silently ignore — dep row stays in list
    } finally {
      setDeletingId(null);
    }
  };

  // ---- Render --------------------------------------------------------------

  if (loading) {
    return (
      <div className={cn("space-y-2", className)}>
        <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Loading dependencies...
        </div>
      </div>
    );
  }

  const rows: DisplayRow[] = [
    ...outgoing.map((dep) => ({
      dep,
      group: outgoingGroup(dep.dependency_type),
      ownerTaskId: taskId,
      relatedTaskId: dep.depends_on_task_id,
    })),
    ...incoming.map((dep) => ({
      dep,
      group: incomingGroup(dep.dependency_type),
      ownerTaskId: dep.task_id,
      relatedTaskId: dep.task_id,
    })),
  ];

  const grouped = rows.reduce<Record<DisplayGroup, DisplayRow[]>>(
    (acc, row) => {
      if (!acc[row.group]) acc[row.group] = [];
      acc[row.group]!.push(row);
      return acc;
    },
    {} as Record<DisplayGroup, DisplayRow[]>,
  );

  const selectedTypeConfig = CREATE_TYPE_CONFIG[form.dependency_type];

  return (
    <div className={cn("space-y-3", className)}>
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Link2 className="h-4 w-4" />
          Dependencies
          {rows.length > 0 && (
            <span className="text-xs text-muted-foreground">
              ({rows.length})
            </span>
          )}
        </div>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1 text-xs"
          onClick={() => {
            setShowForm((v) => !v);
            setError(null);
          }}
        >
          <Plus className="h-3 w-3" />
          Add
        </Button>
      </div>

      {/* Grouped dependency rows */}
      {rows.length > 0 && (
        <div className="space-y-3">
          {GROUP_ORDER.map((group) => {
            const groupRows = grouped[group];
            if (!groupRows || groupRows.length === 0) return null;
            const cfg = GROUP_CONFIG[group];
            const Icon = cfg.icon;
            return (
              <div key={group}>
                <p className="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                  {cfg.label}
                </p>
                <div className="space-y-1">
                  {groupRows.map((row) => {
                    const status = row.dep.related_task_status_id
                      ? statuses.find((s) => s.id === row.dep.related_task_status_id)
                      : undefined;
                    const title = row.dep.related_task_title;
                    return (
                      <div
                        key={row.dep.id}
                        className="group flex items-center gap-2 rounded-md border border-border bg-card px-2.5 py-1.5"
                      >
                        <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                        {onOpenTask && title ? (
                          <button
                            type="button"
                            className="flex-1 truncate text-left text-xs hover:underline"
                            onClick={() => onOpenTask(row.relatedTaskId)}
                            title={title}
                          >
                            {title}
                          </button>
                        ) : (
                          <span className="flex-1 truncate font-mono text-xs text-muted-foreground">
                            {title ?? row.relatedTaskId.slice(0, 8).toUpperCase()}
                          </span>
                        )}
                        {status && (
                          <span
                            className="h-2 w-2 shrink-0 rounded-full"
                            style={{ backgroundColor: status.color }}
                            title={status.name}
                          />
                        )}
                        <Badge
                          className={cn(
                            "shrink-0 px-1.5 py-0 text-[10px] font-medium",
                            cfg.badgeClass,
                          )}
                        >
                          {cfg.label}
                        </Badge>
                        <button
                          type="button"
                          aria-label="Remove dependency"
                          onClick={() => void handleDelete(row)}
                          disabled={deletingId === row.dep.id}
                          className="ml-1 shrink-0 rounded p-0.5 opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100 disabled:cursor-wait"
                        >
                          {deletingId === row.dep.id ? (
                            <Loader2 className="h-3 w-3 animate-spin" />
                          ) : (
                            <X className="h-3 w-3" />
                          )}
                        </button>
                      </div>
                    );
                  })}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {rows.length === 0 && !showForm && (
        <p className="text-xs text-muted-foreground">No dependencies yet.</p>
      )}

      {/* Add form */}
      {showForm && (
        <form
          onSubmit={(e) => void handleAdd(e)}
          className="space-y-2 rounded-lg border border-border bg-muted/20 p-3"
        >
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              Task ID (UUID)
            </label>
            <Input
              value={form.depends_on_task_id}
              onChange={(e) =>
                setForm((f) => ({ ...f, depends_on_task_id: e.target.value }))
              }
              placeholder="550e8400-e29b-41d4-a716-446655440000"
              className="h-7 font-mono text-xs"
              autoFocus
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              Relationship type
            </label>
            <Select
              value={form.dependency_type}
              onChange={(e) =>
                setForm((f) => ({
                  ...f,
                  dependency_type: e.target.value as DependencyType,
                }))
              }
              className="h-7 text-xs"
            >
              {CREATE_TYPE_ORDER.map((type) => (
                <option key={type} value={type}>
                  {CREATE_TYPE_CONFIG[type].label}
                </option>
              ))}
            </Select>
            {/* Direction is the #1 source of silent mistakes here — is_child_of
                especially, since picking the wrong task ID quietly inverts the
                hierarchy with no error. Spell out what the entered task becomes. */}
            <p className="mt-1 text-[11px] text-muted-foreground">
              {selectedTypeConfig.hint}
            </p>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex gap-2">
            <Button
              type="submit"
              size="sm"
              className="flex-1"
              disabled={submitting || !form.depends_on_task_id.trim()}
            >
              {submitting ? (
                <>
                  <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                  Adding...
                </>
              ) : (
                "Add Dependency"
              )}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                setShowForm(false);
                setError(null);
                setForm({ depends_on_task_id: "", dependency_type: "blocks" });
              }}
            >
              Cancel
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
