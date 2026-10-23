import { useState, useEffect, useRef } from "react";
import { Link, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowRight, ArrowLeft, Link2, GitMerge, Plus, X, Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { DependencyTaskPicker, type DependencyTarget } from "@/components/dependency-task-picker";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/cn";
import { useProjectStore } from "@/stores/project";
import { useTaskDependencies, refreshTaskDependencies, type TaskDependenciesSource, type DependencyRow } from "@/hooks/use-task-dependencies";
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
  { icon: typeof ArrowRight; badgeClass: string }
> = {
  blocked_by: {
    icon: ArrowLeft,
    badgeClass: "bg-tone-danger-100 text-tone-danger-700 dark:bg-tone-danger-900/40 dark:text-tone-danger-300",
  },
  blocks: {
    icon: ArrowRight,
    badgeClass: "bg-tone-danger-100 text-tone-danger-700 dark:bg-tone-danger-900/40 dark:text-tone-danger-300",
  },
  relates_to: {
    icon: Link2,
    badgeClass: "bg-tone-info-100 text-tone-info-700 dark:bg-tone-info-900/40 dark:text-tone-info-300",
  },
  child_of: {
    icon: GitMerge,
    badgeClass:
      "bg-tone-warning-100 text-tone-warning-700 dark:bg-tone-warning-900/40 dark:text-tone-warning-300",
  },
  parent_of: {
    icon: GitMerge,
    badgeClass:
      "bg-tone-warning-100 text-tone-warning-700 dark:bg-tone-warning-900/40 dark:text-tone-warning-300",
  },
};

const GROUP_ORDER: DisplayGroup[] = [
  "blocked_by",
  "blocks",
  "relates_to",
  "child_of",
  "parent_of",
];

type CreateRelationship = "depends_on" | "blocks" | "related";
const CREATE_TYPE_ORDER: CreateRelationship[] = ["depends_on", "blocks", "related"];

type DisplayRow = DependencyRow;

// Labels resolve at render: t(`deps.group.${group}`), t(`deps.type.${type}.label|hint`).

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface DependencyListProps {
  taskId: string;
  source?: TaskDependenciesSource;
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
  source: sharedSource,
  className,
  onChanged,
  onOpenTask,
}: DependencyListProps) {
  const { t } = useTranslation("task");
  const { currentProject } = useProjectStore();
  const { wsSlug } = useParams();
  const ownSource = useTaskDependencies(sharedSource ? null : taskId);
  const source = sharedSource ?? ownSource;
  const { rows, reload: fetchDeps } = source;
  const currentTaskId = useRef<string | null>(taskId);
  currentTaskId.current = taskId;
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<DependencyTarget | null>(null);

  const [form, setForm] = useState<{
    dependency_type: CreateRelationship;
  }>({
    dependency_type: "depends_on",
  });

  useEffect(() => {
    currentTaskId.current = taskId;
    setShowForm(false);
    setError(null);
    setSubmitting(false);
    setDeletingId(null);
    setConfirmDeleteId(null);
    setSelected(null);
    setForm({ dependency_type: "depends_on" });
    return () => { currentTaskId.current = null; };
  }, [taskId]);

  // ---- Actions -------------------------------------------------------------

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!selected || selected.task.id === taskId) {
      setError(t("deps.chooseTask"));
      return;
    }
    setSubmitting(true);
    setError(null);
    const targetId = selected.task.id;
    const ownerId = form.dependency_type === "blocks" ? targetId : taskId;
    try {
      await api(`/api/v1/tasks/${ownerId}/dependencies`, {
        method: "POST",
        body: {
          depends_on_task_id: form.dependency_type === "blocks" ? taskId : targetId,
          dependency_type: form.dependency_type === "related" ? "relates_to" : "blocks",
        },
      });
      // The tab can unmount while its shared parent source remains active.
      if (source.isCurrent()) onChanged?.();
      await refreshTaskDependencies(taskId, targetId);
      if (currentTaskId.current !== taskId) return;
      setShowForm(false);
      setSelected(null);
      setForm({ dependency_type: "depends_on" });
    } catch (err: unknown) {
      if (currentTaskId.current !== taskId) return;
      setError(
        apiErrorMessage(err, t("deps.addFailed")),
      );
    } finally {
      if (currentTaskId.current === taskId) setSubmitting(false);
    }
  };

  const handleDelete = async (row: DisplayRow) => {
    setDeletingId(row.dep.id);
    setError(null);
    try {
      // An incoming edge is owned by the OTHER task (it's the one that has
      // task_id = that task, depends_on_task_id = this one), so the delete
      // route has to target that task, not the one this component was mounted
      // for.
      await api(`/api/v1/tasks/${row.ownerTaskId}/dependencies/${row.dep.id}`, {
        method: "DELETE",
      });
      if (source.isCurrent()) onChanged?.();
      if (currentTaskId.current === taskId) setConfirmDeleteId(null);
      await refreshTaskDependencies(row.dep.task_id, row.dep.depends_on_task_id);
    } catch (err) {
      if (currentTaskId.current === taskId) setError(apiErrorMessage(err, t("deps.removeFailed")));
    } finally {
      if (currentTaskId.current === taskId) setDeletingId(null);
    }
  };

  // ---- Render --------------------------------------------------------------

  if (source.phase === "loading") {
    return (
      <div className={cn("space-y-2", className)}>
        <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          {t("deps.loading")}
        </div>
      </div>
    );
  }

  if (source.phase === "error") {
    return <div className={cn("space-y-3", className)} role="alert">
      <p className="text-sm text-destructive">{t("deps.loadFailed")}</p>
      <Button size="sm" onClick={() => void fetchDeps()}>{t("deps.retry")}</Button>
    </div>;
  }

  const grouped = rows.reduce<Record<DisplayGroup, DisplayRow[]>>(
    (acc, row) => {
      if (!acc[row.group]) acc[row.group] = [];
      acc[row.group]!.push(row);
      return acc;
    },
    {} as Record<DisplayGroup, DisplayRow[]>,
  );


  return (
    <div className={cn("space-y-3", className)}>
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Link2 className="h-4 w-4" />
          {t("panel.tabs.dependencies")}
          {wsSlug && currentProject && <Link className="ml-2 underline underline-offset-2" to={`/w/${wsSlug}/p/${currentProject.slug}/timeline?focus=${taskId}`}>{t("deps.view")}</Link>}
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
          {t("deps.add")}
        </Button>
      </div>

      {source.rows.some(row => row.metadataError) && <div role="alert" className="text-xs text-muted-foreground">{t("deps.metadataError")} <button className="underline" onClick={() => void fetchDeps()}>{t("deps.retry")}</button></div>}
      {error && !showForm && <p role="alert" className="text-xs text-destructive">{error}</p>}
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
                  {t(`deps.group.${group}`)}
                </p>
                <div className="space-y-1">
                  {groupRows.map((row) => {
                    const status = row.status;
                    const title = row.task?.title ?? row.dep.related_task_title ?? t("deps.row.taskUnavailable");
                    const open = row.group === "blocked_by" && !!status && !["done", "cancelled"].includes(status.category);
                    const unknown = row.group === "blocked_by" && !status;
                    return (
                      <div
                        key={row.dep.id}
                        className={cn("group flex items-start gap-2 rounded-md border border-border bg-card px-2.5 py-2", open && "border-tone-danger-300 bg-tone-danger-50 dark:border-tone-danger-900 dark:bg-tone-danger-950/30", unknown && "border-tone-warning-300 dark:border-tone-warning-900")}
                      >
                        <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                        <div className="min-w-0 flex-1 space-y-1">
                          {onOpenTask ? (
                            <button type="button" className="block w-full text-left text-xs hover:underline break-words" onClick={() => onOpenTask(row.relatedTaskId)}>
                              <span className="font-mono text-muted-foreground">#{row.relatedTaskId.slice(0, 8)}</span>{" "}{title}
                            </button>
                          ) : <p className="break-words text-xs">#{row.relatedTaskId.slice(0, 8)} {title}</p>}
                          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                            <span>{status?.name ?? (source.checking ? t("deps.row.loadingStatus") : t("deps.row.statusUnavailable"))}</span>
                            <span>{row.task ? row.task.assignee_name ?? (row.task.assignee_id ? t("deps.row.assigneeUnavailable") : t("panel.props.unassigned")) : t("deps.row.assigneeUnavailable")}</span>
                            {open && <Badge className={cn("text-[10px]", cfg.badgeClass)}>{t("deps.row.openBlocker")}</Badge>}
                            {unknown && !source.checking && <span>{t("deps.row.blockerUnknown")}</span>}
                          </div>
                        </div>
                        <button
                          type="button"
                          aria-label={t("deps.row.remove")}
                          onClick={() => setConfirmDeleteId(row.dep.id)}
                          disabled={deletingId === row.dep.id}
                          className="ml-1 flex h-7 w-7 shrink-0 items-center justify-center rounded transition-colors hover:text-destructive focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-wait"
                        >
                          {deletingId === row.dep.id ? (
                            <Loader2 className="h-3 w-3 animate-spin" />
                          ) : (
                            <X className="h-3 w-3" />
                          )}
                        </button>
                        {confirmDeleteId === row.dep.id && <div className="flex shrink-0 flex-col gap-1 text-xs">
                          <span>{t("deps.row.removeConfirm")}</span>
                          <Button size="sm" variant="destructive" disabled={deletingId === row.dep.id} onClick={() => void handleDelete(row)}>{t("deps.row.removeAction")}</Button>
                          <Button size="sm" variant="ghost" disabled={deletingId === row.dep.id} onClick={() => setConfirmDeleteId(null)}>{t("actions.cancel")}</Button>
                        </div>}
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
        <p className="text-xs text-muted-foreground">{t("deps.empty")}</p>
      )}

      {/* Add form */}
      {showForm && (
        <form
          onSubmit={(e) => void handleAdd(e)}
          className="space-y-2 rounded-lg border border-border bg-muted/20 p-3"
        >
          <DependencyTaskPicker taskId={taskId} disabled={submitting} selected={selected} onSelect={setSelected} />
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              {t("deps.form.type")}
            </label>
            <Select
              aria-label={t("deps.form.type")}
              disabled={submitting}
              value={form.dependency_type}
              onChange={(e) =>
                setForm((f) => ({
                  ...f,
                  dependency_type: e.target.value as CreateRelationship,
                }))
              }
              className="h-7 text-xs"
            >
              {CREATE_TYPE_ORDER.map((type) => (
                <option key={type} value={type}>
                  {t(`deps.type.${type}.label`)}
                </option>
              ))}
            </Select>
            <p className="mt-1 text-[11px] text-muted-foreground">
              {t(`deps.type.${form.dependency_type}.hint`)}
            </p>
          </div>
          {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
          <div className="flex gap-2">
            <Button
              type="submit"
              size="sm"
              className="flex-1"
              disabled={submitting || !selected}
            >
              {submitting ? (
                <>
                  <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                  {t("subtasks.adding")}
                </>
              ) : (
                t("deps.form.submit")
              )}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={submitting}
              onClick={() => {
                setShowForm(false);
                setSelected(null);
                setError(null);
                setForm({ dependency_type: "depends_on" });
              }}
            >
              {t("actions.cancel")}
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
