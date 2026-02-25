import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Bot,
  ChevronLeft,
  ChevronRight,
  Columns3,
  GitBranch,
  List,
  Loader2,
  Trash2,
  User,
  X,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { CustomFieldRenderer } from "@/components/custom-field-renderer";
import { SavedViewsMenu } from "@/components/saved-views-menu";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { formatDate, priorityConfig } from "@/lib/utils";
import type { CustomFieldDefinition, Priority, SavedView, Task, UpdateTaskRequest } from "@/types";

// ---------------------------------------------------------------------------
// Sort configuration
// ---------------------------------------------------------------------------

type SortField = "title" | "status" | "priority" | "assignee" | "due_date" | string;
type SortDir = "asc" | "desc";

const PRIORITY_ORDER: Record<Priority, number> = {
  urgent: 0,
  high: 1,
  medium: 2,
  low: 3,
  none: 4,
};

const PRIORITY_OPTIONS: { value: Priority; label: string }[] = [
  { value: "urgent", label: "Urgent" },
  { value: "high", label: "High" },
  { value: "medium", label: "Medium" },
  { value: "low", label: "Low" },
  { value: "none", label: "None" },
];

const DEFAULT_PER_PAGE = 50;

// ---------------------------------------------------------------------------
// Editing cell identifier
// ---------------------------------------------------------------------------

interface EditingCell {
  taskId: string;
  field: string;
}

// ---------------------------------------------------------------------------
// List View page
// ---------------------------------------------------------------------------

export function ListViewPage() {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();
  const { tasks, isLoading, total, hasMore, fetchTasks, updateTask, deleteTask } =
    useTaskStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();

  const [sortField, setSortField] = useState<SortField>("title");
  const [sortDir, setSortDir] = useState<SortDir>("asc");

  // Selection state
  const [selectedTaskIds, setSelectedTaskIds] = useState<Set<string>>(new Set());

  // Editing cell state
  const [editingCell, setEditingCell] = useState<EditingCell | null>(null);

  // Bulk action state
  const [bulkProgress, setBulkProgress] = useState<{ done: number; total: number } | null>(
    null,
  );
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);

  // Derive pagination from URL search params
  const page = Math.max(1, Number(searchParams.get("page")) || 1);
  const perPage = Math.max(1, Number(searchParams.get("per_page")) || DEFAULT_PER_PAGE);

  useEffect(() => {
    if (currentProject) {
      fetchStatuses(currentProject.id);
      fetchTasks(currentProject.id, { page, page_size: perPage });
      fetchCustomFields(currentProject.id).catch(() => {
        // Custom fields API may not be available yet
      });
    }
  }, [currentProject, fetchStatuses, fetchTasks, fetchCustomFields, page, perPage]);

  // Clear selection when tasks change (page navigation)
  useEffect(() => {
    setSelectedTaskIds(new Set());
  }, [page]);

  // Status lookup
  const statusMap = useMemo(() => {
    const map = new Map<string, { name: string; color: string; position: number }>();
    for (const s of statuses) {
      map.set(s.id, { name: s.name, color: s.color, position: s.position });
    }
    return map;
  }, [statuses]);

  // Sorted custom field defs
  const sortedFieldDefs = useMemo(
    () => [...customFieldDefs].sort((a, b) => a.position - b.position),
    [customFieldDefs],
  );

  // Toggle sort
  const handleSort = useCallback(
    (field: SortField) => {
      if (sortField === field) {
        setSortDir((d) => (d === "asc" ? "desc" : "asc"));
      } else {
        setSortField(field);
        setSortDir("asc");
      }
    },
    [sortField],
  );

  // Sorted tasks
  const sortedTasks = useMemo(() => {
    const arr = [...tasks];
    arr.sort((a, b) => {
      let cmp = 0;

      switch (sortField) {
        case "title":
          cmp = a.title.localeCompare(b.title);
          break;
        case "status": {
          const sa = statusMap.get(a.status_id)?.position ?? 0;
          const sb = statusMap.get(b.status_id)?.position ?? 0;
          cmp = sa - sb;
          break;
        }
        case "priority":
          cmp = PRIORITY_ORDER[a.priority] - PRIORITY_ORDER[b.priority];
          break;
        case "assignee":
          cmp = (a.assignee_type ?? "").localeCompare(b.assignee_type ?? "");
          break;
        case "due_date": {
          const da = a.due_date ? new Date(a.due_date).getTime() : Infinity;
          const db = b.due_date ? new Date(b.due_date).getTime() : Infinity;
          cmp = da - db;
          break;
        }
        default: {
          // Custom field sort -- type-aware
          const fieldDef = sortedFieldDefs.find((f) => f.slug === sortField);
          const va = a.custom_fields?.[sortField];
          const vb = b.custom_fields?.[sortField];

          if (fieldDef?.field_type === "number") {
            const na = va != null ? Number(va) : -Infinity;
            const nb = vb != null ? Number(vb) : -Infinity;
            cmp = na - nb;
          } else if (
            fieldDef?.field_type === "date" ||
            fieldDef?.field_type === "datetime"
          ) {
            const da = va ? new Date(String(va)).getTime() : 0;
            const db = vb ? new Date(String(vb)).getTime() : 0;
            cmp = da - db;
          } else if (fieldDef?.field_type === "checkbox") {
            cmp = (va ? 1 : 0) - (vb ? 1 : 0);
          } else {
            const strA = va != null ? String(va) : "";
            const strB = vb != null ? String(vb) : "";
            cmp = strA.localeCompare(strB);
          }
          break;
        }
      }

      return sortDir === "asc" ? cmp : -cmp;
    });
    return arr;
  }, [tasks, sortField, sortDir, statusMap, sortedFieldDefs]);

  // ---------------------------------------------------------------------------
  // Selection helpers
  // ---------------------------------------------------------------------------

  const allSelected =
    sortedTasks.length > 0 &&
    sortedTasks.every((t) => selectedTaskIds.has(t.id));
  const someSelected = selectedTaskIds.size > 0;

  const toggleSelectAll = useCallback(() => {
    if (allSelected) {
      setSelectedTaskIds(new Set());
    } else {
      setSelectedTaskIds(new Set(sortedTasks.map((t) => t.id)));
    }
  }, [allSelected, sortedTasks]);

  const toggleSelectTask = useCallback((taskId: string) => {
    setSelectedTaskIds((prev) => {
      const next = new Set(prev);
      if (next.has(taskId)) {
        next.delete(taskId);
      } else {
        next.add(taskId);
      }
      return next;
    });
  }, []);

  // ---------------------------------------------------------------------------
  // Inline editing helpers
  // ---------------------------------------------------------------------------

  const startEdit = useCallback(
    (taskId: string, field: string) => {
      // Close any open edit first
      setEditingCell({ taskId, field });
    },
    [],
  );

  const cancelEdit = useCallback(() => {
    setEditingCell(null);
  }, []);

  const saveCell = useCallback(
    async (taskId: string, req: UpdateTaskRequest) => {
      setEditingCell(null);
      try {
        await updateTask(taskId, req);
        toast.success("Task updated");
      } catch {
        toast.error("Failed to update task");
      }
    },
    [updateTask],
  );

  // ---------------------------------------------------------------------------
  // Bulk action helpers
  // ---------------------------------------------------------------------------

  const runBulkUpdate = useCallback(
    async (updates: UpdateTaskRequest) => {
      const ids = Array.from(selectedTaskIds);
      setBulkProgress({ done: 0, total: ids.length });

      let done = 0;
      const results = await Promise.allSettled(
        ids.map(async (id) => {
          await updateTask(id, updates);
          done += 1;
          setBulkProgress({ done, total: ids.length });
        }),
      );

      const failed = results.filter((r) => r.status === "rejected").length;
      setBulkProgress(null);
      setSelectedTaskIds(new Set());

      if (failed === 0) {
        toast.success(`Updated ${ids.length} task${ids.length !== 1 ? "s" : ""}`);
      } else {
        toast.error(`${failed} task${failed !== 1 ? "s" : ""} failed to update`);
      }

      if (currentProject) {
        await fetchTasks(currentProject.id, { page, page_size: perPage });
      }
    },
    [selectedTaskIds, updateTask, currentProject, fetchTasks, page, perPage],
  );

  const runBulkDelete = useCallback(async () => {
    const ids = Array.from(selectedTaskIds);
    setBulkProgress({ done: 0, total: ids.length });
    setDeleteConfirmOpen(false);

    let done = 0;
    const results = await Promise.allSettled(
      ids.map(async (id) => {
        await deleteTask(id);
        done += 1;
        setBulkProgress({ done, total: ids.length });
      }),
    );

    const failed = results.filter((r) => r.status === "rejected").length;
    setBulkProgress(null);
    setSelectedTaskIds(new Set());

    if (failed === 0) {
      toast.success(`Deleted ${ids.length} task${ids.length !== 1 ? "s" : ""}`);
    } else {
      toast.error(`${failed} task${failed !== 1 ? "s" : ""} failed to delete`);
    }

    if (currentProject) {
      await fetchTasks(currentProject.id, { page, page_size: perPage });
    }
  }, [selectedTaskIds, deleteTask, currentProject, fetchTasks, page, perPage]);

  // ---------------------------------------------------------------------------
  // Navigation
  // ---------------------------------------------------------------------------

  const handleTaskClick = useCallback(
    (task: Task) => {
      // Don't navigate if editing or selecting
      if (editingCell?.taskId === task.id) return;
      navigate(`/w/${wsSlug}/p/${projectSlug}/t/${task.id}`);
    },
    [navigate, wsSlug, projectSlug, editingCell],
  );

  // Apply a saved view: restore sort from its configuration.
  const handleApplyView = useCallback((view: SavedView) => {
    if (view.sort_by) {
      setSortField(view.sort_by as SortField);
    }
    if (view.sort_order === "asc" || view.sort_order === "desc") {
      setSortDir(view.sort_order);
    }
  }, []);

  // Pagination helpers
  const totalPages = Math.max(1, Math.ceil(total / perPage));
  const rangeStart = total > 0 ? (page - 1) * perPage + 1 : 0;
  const rangeEnd = Math.min(page * perPage, total);

  const goToPage = useCallback(
    (newPage: number) => {
      const params = new URLSearchParams(searchParams);
      params.set("page", String(newPage));
      if (perPage !== DEFAULT_PER_PAGE) {
        params.set("per_page", String(perPage));
      }
      setSearchParams(params);
    },
    [searchParams, setSearchParams, perPage],
  );

  if (!currentProject) {
    return (
      <div className="flex items-center justify-center py-12">
        <p className="text-muted-foreground">
          Project &quot;{projectSlug}&quot; not found
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <List className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">
            {currentProject.name}
          </h1>
        </div>

        <div className="flex items-center gap-2">
          {/* Saved views */}
          <SavedViewsMenu
            projectId={currentProject.id}
            currentViewType="list"
            currentSortBy={sortField}
            currentSortOrder={sortDir}
            onApplyView={handleApplyView}
          />

          {/* View toggle */}
          <div className="flex items-center gap-1 rounded-lg border border-border bg-muted/50 p-1">
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1.5 px-3 text-xs"
              onClick={() => navigate(`/w/${wsSlug}/p/${projectSlug}`)}
            >
              <Columns3 className="h-3.5 w-3.5" />
              Board
            </Button>
            <Button
              variant="secondary"
              size="sm"
              className="h-7 gap-1.5 px-3 text-xs"
            >
              <List className="h-3.5 w-3.5" />
              List
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1.5 px-3 text-xs"
              onClick={() =>
                navigate(`/w/${wsSlug}/p/${projectSlug}/timeline`)
              }
            >
              <GitBranch className="h-3.5 w-3.5" />
              Timeline
            </Button>
          </div>
        </div>
      </div>

      {/* Table */}
      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full rounded-lg" />
          ))}
        </div>
      ) : tasks.length === 0 && page === 1 ? (
        <div className="flex flex-col items-center justify-center py-20">
          <List className="mb-4 h-12 w-12 text-muted-foreground" />
          <h3 className="mb-2 text-lg font-semibold">No tasks yet</h3>
          <p className="text-sm text-muted-foreground">
            Create tasks to see them in list view.
          </p>
        </div>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/50">
                  {/* Checkbox column */}
                  <th className="w-10 px-3 py-2">
                    <input
                      type="checkbox"
                      checked={allSelected}
                      ref={(el) => {
                        if (el) {
                          el.indeterminate =
                            someSelected && !allSelected;
                        }
                      }}
                      onChange={toggleSelectAll}
                      className="h-4 w-4 rounded border-input cursor-pointer"
                      aria-label="Select all tasks"
                    />
                  </th>
                  <SortableHeader
                    label="Title"
                    field="title"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[240px]"
                  />
                  <SortableHeader
                    label="Status"
                    field="status"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[120px]"
                  />
                  <SortableHeader
                    label="Priority"
                    field="priority"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[100px]"
                  />
                  <SortableHeader
                    label="Assignee"
                    field="assignee"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[100px]"
                  />
                  <SortableHeader
                    label="Due Date"
                    field="due_date"
                    currentField={sortField}
                    dir={sortDir}
                    onSort={handleSort}
                    className="min-w-[110px]"
                  />
                  {/* Dynamic custom field columns */}
                  {sortedFieldDefs.map((field) => (
                    <SortableHeader
                      key={field.id}
                      label={field.name}
                      field={field.slug}
                      currentField={sortField}
                      dir={sortDir}
                      onSort={handleSort}
                      className="min-w-[100px]"
                    />
                  ))}
                </tr>
              </thead>
              <tbody>
                {sortedTasks.map((task) => {
                  const isSelected = selectedTaskIds.has(task.id);

                  return (
                    <tr
                      key={task.id}
                      className={cn(
                        "border-b border-border transition-colors last:border-b-0",
                        isSelected
                          ? "bg-primary/5"
                          : "hover:bg-muted/30",
                      )}
                    >
                      {/* Checkbox */}
                      <td
                        className="w-10 px-3 py-2"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <input
                          type="checkbox"
                          checked={isSelected}
                          onChange={() => toggleSelectTask(task.id)}
                          className="h-4 w-4 rounded border-input cursor-pointer"
                          aria-label={`Select task ${task.title}`}
                        />
                      </td>

                      {/* Title */}
                      <TitleCell
                        task={task}
                        isEditing={
                          editingCell?.taskId === task.id &&
                          editingCell.field === "title"
                        }
                        onStartEdit={() => startEdit(task.id, "title")}
                        onSave={(title) => saveCell(task.id, { title })}
                        onCancel={cancelEdit}
                        onNavigate={() => handleTaskClick(task)}
                      />

                      {/* Status */}
                      <StatusCell
                        task={task}
                        statusMap={statusMap}
                        statuses={statuses}
                        isEditing={
                          editingCell?.taskId === task.id &&
                          editingCell.field === "status"
                        }
                        onStartEdit={() => startEdit(task.id, "status")}
                        onSave={(statusId) =>
                          saveCell(task.id, { status_id: statusId })
                        }
                        onCancel={cancelEdit}
                      />

                      {/* Priority */}
                      <PriorityCell
                        task={task}
                        isEditing={
                          editingCell?.taskId === task.id &&
                          editingCell.field === "priority"
                        }
                        onStartEdit={() => startEdit(task.id, "priority")}
                        onSave={(priority) =>
                          saveCell(task.id, { priority: priority as Priority })
                        }
                        onCancel={cancelEdit}
                      />

                      {/* Assignee (read-only for now — needs user/agent picker) */}
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-1">
                          {task.assignee_type === "agent" ? (
                            <Bot className="h-3.5 w-3.5 text-violet-500" />
                          ) : task.assignee_type === "user" ? (
                            <User className="h-3.5 w-3.5 text-sky-500" />
                          ) : (
                            <span className="text-xs text-muted-foreground">
                              --
                            </span>
                          )}
                          {task.assignee_id && (
                            <span className="text-xs text-muted-foreground">
                              {task.assignee_id.slice(0, 8)}
                            </span>
                          )}
                        </div>
                      </td>

                      {/* Due Date */}
                      <DueDateCell
                        task={task}
                        isEditing={
                          editingCell?.taskId === task.id &&
                          editingCell.field === "due_date"
                        }
                        onStartEdit={() => startEdit(task.id, "due_date")}
                        onSave={(due_date) => saveCell(task.id, { due_date })}
                        onCancel={cancelEdit}
                      />

                      {/* Custom field columns */}
                      {sortedFieldDefs.map((field) => (
                        <CustomFieldCell
                          key={field.id}
                          task={task}
                          field={field}
                          isEditing={
                            editingCell?.taskId === task.id &&
                            editingCell.field === field.slug
                          }
                          onStartEdit={() =>
                            startEdit(task.id, field.slug)
                          }
                          onSave={(value) =>
                            saveCell(task.id, {
                              custom_fields: {
                                ...task.custom_fields,
                                [field.slug]: value,
                              },
                            })
                          }
                          onCancel={cancelEdit}
                        />
                      ))}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>

          {/* Pagination footer */}
          {total > 0 && (
            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span>
                Showing {rangeStart}&#8211;{rangeEnd} of {total} task
                {total !== 1 ? "s" : ""}
                {sortedFieldDefs.length > 0 &&
                  ` | ${sortedFieldDefs.length} custom field${sortedFieldDefs.length !== 1 ? "s" : ""}`}
              </span>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={page <= 1}
                  onClick={() => goToPage(page - 1)}
                >
                  <ChevronLeft className="mr-0.5 h-3.5 w-3.5" />
                  Previous
                </Button>
                <span className="tabular-nums">
                  Page {page} of {totalPages}
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={!hasMore && page >= totalPages}
                  onClick={() => goToPage(page + 1)}
                >
                  Next
                  <ChevronRight className="ml-0.5 h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          )}
        </>
      )}

      {/* Bulk action bar */}
      {someSelected && (
        <BulkActionBar
          selectedCount={selectedTaskIds.size}
          statuses={statuses}
          progress={bulkProgress}
          onClearSelection={() => setSelectedTaskIds(new Set())}
          onBulkStatus={(statusId) => runBulkUpdate({ status_id: statusId })}
          onBulkPriority={(priority) =>
            runBulkUpdate({ priority: priority as Priority })
          }
          onBulkDelete={() => setDeleteConfirmOpen(true)}
        />
      )}

      {/* Delete confirmation dialog */}
      <Dialog
        open={deleteConfirmOpen}
        onOpenChange={setDeleteConfirmOpen}
      >
        <DialogContent onClose={() => setDeleteConfirmOpen(false)}>
          <DialogHeader>
            <DialogTitle>Delete tasks</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete {selectedTaskIds.size} task
              {selectedTaskIds.size !== 1 ? "s" : ""}? This action cannot be
              undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setDeleteConfirmOpen(false)}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={runBulkDelete}
            >
              <Trash2 className="mr-1.5 h-3.5 w-3.5" />
              Delete {selectedTaskIds.size} task
              {selectedTaskIds.size !== 1 ? "s" : ""}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Bulk action bar
// ---------------------------------------------------------------------------

interface BulkActionBarProps {
  selectedCount: number;
  statuses: { id: string; name: string; color: string }[];
  progress: { done: number; total: number } | null;
  onClearSelection: () => void;
  onBulkStatus: (statusId: string) => void;
  onBulkPriority: (priority: string) => void;
  onBulkDelete: () => void;
}

function BulkActionBar({
  selectedCount,
  statuses,
  progress,
  onClearSelection,
  onBulkStatus,
  onBulkPriority,
  onBulkDelete,
}: BulkActionBarProps) {
  return (
    <div className="fixed bottom-6 left-1/2 z-40 -translate-x-1/2">
      <div className="flex items-center gap-3 rounded-xl border border-border bg-card px-4 py-2.5 shadow-lg">
        {progress ? (
          <div className="flex items-center gap-2 text-sm">
            <Loader2 className="h-4 w-4 animate-spin text-primary" />
            <span>
              Updating {progress.done}/{progress.total} tasks...
            </span>
          </div>
        ) : (
          <>
            <span className="text-sm font-medium">
              {selectedCount} task{selectedCount !== 1 ? "s" : ""} selected
            </span>

            <div className="h-4 w-px bg-border" />

            {/* Move to status */}
            <Select
              className="h-8 w-40 text-xs"
              value=""
              onChange={(e) => {
                if (e.target.value) {
                  onBulkStatus(e.target.value);
                  e.target.value = "";
                }
              }}
            >
              <option value="">Move to status...</option>
              {statuses.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>

            {/* Set priority */}
            <Select
              className="h-8 w-36 text-xs"
              value=""
              onChange={(e) => {
                if (e.target.value) {
                  onBulkPriority(e.target.value);
                  e.target.value = "";
                }
              }}
            >
              <option value="">Set priority...</option>
              {PRIORITY_OPTIONS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </Select>

            <div className="h-4 w-px bg-border" />

            {/* Delete */}
            <Button
              variant="ghost"
              size="sm"
              className="h-8 gap-1.5 px-2 text-xs text-destructive hover:text-destructive"
              onClick={onBulkDelete}
            >
              <Trash2 className="h-3.5 w-3.5" />
              Delete
            </Button>

            {/* Clear */}
            <Button
              variant="ghost"
              size="sm"
              className="h-8 px-2 text-xs"
              onClick={onClearSelection}
            >
              <X className="h-3.5 w-3.5" />
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Inline editing cell components
// ---------------------------------------------------------------------------

// Title cell
function TitleCell({
  task,
  isEditing,
  onStartEdit,
  onSave,
  onCancel,
  onNavigate,
}: {
  task: Task;
  isEditing: boolean;
  onStartEdit: () => void;
  onSave: (title: string) => void;
  onCancel: () => void;
  onNavigate: () => void;
}) {
  const [draft, setDraft] = useState(task.title);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (isEditing) {
      setDraft(task.title);
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [isEditing, task.title]);

  if (isEditing) {
    return (
      <td
        className="px-3 py-2"
        onClick={(e) => e.stopPropagation()}
      >
        <Input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          className="h-7 text-xs font-medium"
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              if (draft.trim()) onSave(draft.trim());
              else onCancel();
            }
            if (e.key === "Escape") {
              onCancel();
            }
          }}
          onBlur={() => {
            if (draft.trim() && draft.trim() !== task.title) {
              onSave(draft.trim());
            } else {
              onCancel();
            }
          }}
        />
      </td>
    );
  }

  return (
    <td
      className="cursor-pointer px-3 py-2"
      onDoubleClick={(e) => {
        e.stopPropagation();
        onStartEdit();
      }}
      onClick={onNavigate}
    >
      <span className="font-medium hover:text-primary">{task.title}</span>
    </td>
  );
}

// Status cell
function StatusCell({
  task,
  statusMap,
  statuses,
  isEditing,
  onStartEdit,
  onSave,
  onCancel,
}: {
  task: Task;
  statusMap: Map<string, { name: string; color: string; position: number }>;
  statuses: { id: string; name: string; color: string }[];
  isEditing: boolean;
  onStartEdit: () => void;
  onSave: (statusId: string) => void;
  onCancel: () => void;
}) {
  const status = statusMap.get(task.status_id);

  if (isEditing) {
    return (
      <td
        className="px-3 py-2"
        onClick={(e) => e.stopPropagation()}
      >
        <Select
          className="h-7 text-xs"
          value={task.status_id}
          onChange={(e) => {
            if (e.target.value) onSave(e.target.value);
            else onCancel();
          }}
          autoFocus
          onBlur={onCancel}
          onKeyDown={(e) => {
            if (e.key === "Escape") onCancel();
          }}
        >
          {statuses.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </Select>
      </td>
    );
  }

  return (
    <td
      className="cursor-pointer px-3 py-2"
      onClick={(e) => {
        e.stopPropagation();
        onStartEdit();
      }}
    >
      {status && (
        <div className="flex items-center gap-1.5">
          <span
            className="inline-block h-2 w-2 rounded-full"
            style={{ backgroundColor: status.color }}
          />
          <span className="text-xs">{status.name}</span>
        </div>
      )}
    </td>
  );
}

// Priority cell
function PriorityCell({
  task,
  isEditing,
  onStartEdit,
  onSave,
  onCancel,
}: {
  task: Task;
  isEditing: boolean;
  onStartEdit: () => void;
  onSave: (priority: string) => void;
  onCancel: () => void;
}) {
  const pConfig = priorityConfig[task.priority];

  if (isEditing) {
    return (
      <td
        className="px-3 py-2"
        onClick={(e) => e.stopPropagation()}
      >
        <Select
          className="h-7 text-xs"
          value={task.priority}
          onChange={(e) => {
            if (e.target.value) onSave(e.target.value);
            else onCancel();
          }}
          autoFocus
          onBlur={onCancel}
          onKeyDown={(e) => {
            if (e.key === "Escape") onCancel();
          }}
        >
          {PRIORITY_OPTIONS.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </Select>
      </td>
    );
  }

  return (
    <td
      className="cursor-pointer px-3 py-2"
      onClick={(e) => {
        e.stopPropagation();
        onStartEdit();
      }}
    >
      {task.priority !== "none" ? (
        <Badge
          variant="secondary"
          className={cn("text-[10px]", pConfig.color)}
        >
          {pConfig.label}
        </Badge>
      ) : (
        <span className="text-xs text-muted-foreground">--</span>
      )}
    </td>
  );
}

// Due date cell
function DueDateCell({
  task,
  isEditing,
  onStartEdit,
  onSave,
  onCancel,
}: {
  task: Task;
  isEditing: boolean;
  onStartEdit: () => void;
  onSave: (due_date: string | null) => void;
  onCancel: () => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);

  // Normalize due_date to YYYY-MM-DD for the date input
  const dateValue = task.due_date
    ? task.due_date.slice(0, 10)
    : "";

  useEffect(() => {
    if (isEditing) {
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [isEditing]);

  if (isEditing) {
    return (
      <td
        className="px-3 py-2"
        onClick={(e) => e.stopPropagation()}
      >
        <Input
          ref={inputRef}
          type="date"
          className="h-7 text-xs"
          defaultValue={dateValue}
          onKeyDown={(e) => {
            if (e.key === "Escape") onCancel();
            if (e.key === "Enter") {
              const v = (e.target as HTMLInputElement).value;
              onSave(v || null);
            }
          }}
          onBlur={(e) => {
            const v = e.target.value;
            onSave(v || null);
          }}
        />
      </td>
    );
  }

  return (
    <td
      className="cursor-pointer px-3 py-2"
      onClick={(e) => {
        e.stopPropagation();
        onStartEdit();
      }}
    >
      <span className="text-xs">
        {task.due_date ? formatDate(task.due_date) : "--"}
      </span>
    </td>
  );
}

// Custom field cell
function CustomFieldCell({
  task,
  field,
  isEditing,
  onStartEdit,
  onSave,
  onCancel,
}: {
  task: Task;
  field: CustomFieldDefinition;
  isEditing: boolean;
  onStartEdit: () => void;
  onSave: (value: unknown) => void;
  onCancel: () => void;
}) {
  const currentValue = task.custom_fields?.[field.slug];
  const [draft, setDraft] = useState<unknown>(currentValue);

  useEffect(() => {
    if (isEditing) {
      setDraft(currentValue);
    }
  }, [isEditing, currentValue]);

  if (isEditing) {
    return (
      <td
        className="px-3 py-2"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === "Escape") onCancel();
          if (e.key === "Enter" && field.field_type !== "json") {
            e.preventDefault();
            onSave(draft);
          }
        }}
      >
        <CustomFieldRenderer
          field={field}
          value={draft}
          onChange={(v) => {
            setDraft(v);
            // For checkbox and select types, save immediately on change
            if (
              field.field_type === "checkbox" ||
              field.field_type === "select"
            ) {
              onSave(v);
            }
          }}
          compact
        />
        {/* For text/number/url/email/date — save on blur is handled by the inner inputs */}
      </td>
    );
  }

  return (
    <td
      className="cursor-pointer px-3 py-2"
      onClick={(e) => {
        e.stopPropagation();
        onStartEdit();
      }}
    >
      <CustomFieldRenderer
        field={field}
        value={currentValue}
        onChange={() => {}}
        readOnly
        compact
      />
    </td>
  );
}

// ---------------------------------------------------------------------------
// Sortable table header
// ---------------------------------------------------------------------------

function SortableHeader({
  label,
  field,
  currentField,
  dir,
  onSort,
  className,
}: {
  label: string;
  field: string;
  currentField: string;
  dir: SortDir;
  onSort: (field: string) => void;
  className?: string;
}) {
  const isActive = currentField === field;

  return (
    <th
      className={cn(
        "cursor-pointer select-none px-3 py-2 text-xs font-semibold text-muted-foreground transition-colors hover:text-foreground",
        className,
      )}
      onClick={() => onSort(field)}
    >
      <div className="flex items-center gap-1">
        {label}
        {isActive ? (
          dir === "asc" ? (
            <ArrowUp className="h-3 w-3" />
          ) : (
            <ArrowDown className="h-3 w-3" />
          )
        ) : (
          <ArrowUpDown className="h-3 w-3 opacity-30" />
        )}
      </div>
    </th>
  );
}
