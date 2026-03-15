import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useParams, useSearchParams } from "react-router";
import {
  AlignLeft,
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  ChevronDown,
  ChevronRight,
  GitBranch,
  List,
  Loader2,
  Paperclip,
  Plus,
  Trash2,
  X,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
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
import { AssigneeAvatar } from "@/components/assignee-avatar";
import { PriorityFlag } from "@/components/priority-flag";
import { CustomFieldRenderer } from "@/components/custom-field-renderer";
import { TaskSlideOver } from "@/components/task-slide-over";
import { useSavedViewStore } from "@/stores/saved-view-store";
import { ColumnPicker, type ColumnDef } from "@/components/column-picker";
import {
  TagFilterDropdown,
  CustomFieldFilterDialog,
  applyViewFilters,
  type CFFilters,
} from "@/components/view-filters";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { formatDate } from "@/lib/utils";
import { api } from "@/lib/api";
import type {
  CustomFieldDefinition,
  Priority,
  Task,
  TaskStatus,
  UpdateTaskRequest,
} from "@/types";

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
// Status group
// ---------------------------------------------------------------------------

interface StatusGroup {
  status: TaskStatus;
  tasks: Task[];
}

// ---------------------------------------------------------------------------
// List View page
// ---------------------------------------------------------------------------

export function ListViewPage() {
  const { projectSlug } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();
  const { tasks, isLoading, total, hasMore, fetchTasks, updateTask, deleteTask, createTask } =
    useTaskStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();

  const [sortField, setSortField] = useState<SortField>("title");
  const [sortDir, setSortDir] = useState<SortDir>("asc");

  // Slide-over state
  const [slideOverTaskId, setSlideOverTaskId] = useState<string | null>(null);

  // Selection state
  const [selectedTaskIds, setSelectedTaskIds] = useState<Set<string>>(new Set());

  // Editing cell state
  const [editingCell, setEditingCell] = useState<EditingCell | null>(null);

  // Bulk action state
  const [bulkProgress, setBulkProgress] = useState<{ done: number; total: number } | null>(
    null,
  );
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);

  // Group collapsed state: Set of status IDs that are collapsed
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set());

  // Subtask expansion state
  const [expandedTasks, setExpandedTasks] = useState<Set<string>>(new Set());
  const [subtaskMap, setSubtaskMap] = useState<Record<string, Task[]>>({});
  const [loadingSubtasks, setLoadingSubtasks] = useState<Set<string>>(new Set());

  // Inline add task per group
  const [addingInGroup, setAddingInGroup] = useState<string | null>(null);
  const [addingTitle, setAddingTitle] = useState("");
  const addInputRef = useRef<HTMLInputElement>(null);

  // Tag + custom field filter state
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [cfFilters, setCFFilters] = useState<CFFilters>({});

  // Column visibility state
  const DEFAULT_VISIBLE_COLUMNS = new Set([
    "name",
    "status",
    "priority",
    "assignee",
    "due_date",
  ]);
  const [visibleColumns, setVisibleColumns] = useState<Set<string>>(
    DEFAULT_VISIBLE_COLUMNS,
  );

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

  // Clear selection and subtask cache when tasks change (page navigation, project switch)
  useEffect(() => {
    setSelectedTaskIds(new Set());
    setSubtaskMap({});
    setExpandedTasks(new Set());
  }, [page, currentProject]);

  // Focus add input when a group add row opens
  useEffect(() => {
    if (addingInGroup !== null) {
      setTimeout(() => addInputRef.current?.focus(), 0);
    }
  }, [addingInGroup]);

  // Status lookup map: id -> status
  const statusMap = useMemo(() => {
    const map = new Map<string, TaskStatus>();
    for (const s of statuses) {
      map.set(s.id, s);
    }
    return map;
  }, [statuses]);

  // Sorted custom field defs
  const sortedFieldDefs = useMemo(
    () => [...customFieldDefs].sort((a, b) => a.position - b.position),
    [customFieldDefs],
  );

  // Build allColumns from static defs + dynamic custom fields
  const allColumns = useMemo((): ColumnDef[] => {
    const staticCols: ColumnDef[] = [
      { key: "name", label: "Name", visible: true, required: true },
      { key: "status", label: "Status", visible: visibleColumns.has("status") },
      { key: "priority", label: "Priority", visible: visibleColumns.has("priority") },
      { key: "assignee", label: "Assignee", visible: visibleColumns.has("assignee") },
      { key: "due_date", label: "Due Date", visible: visibleColumns.has("due_date") },
      { key: "labels", label: "Labels", visible: visibleColumns.has("labels") },
      {
        key: "estimated_hours",
        label: "Estimate",
        visible: visibleColumns.has("estimated_hours"),
      },
      { key: "created_at", label: "Created", visible: visibleColumns.has("created_at") },
    ];
    const cfCols: ColumnDef[] = sortedFieldDefs.map((f) => ({
      key: `cf:${f.slug}`,
      label: f.name,
      visible: visibleColumns.has(`cf:${f.slug}`),
    }));
    return [...staticCols, ...cfCols];
  }, [sortedFieldDefs, visibleColumns]);

  const handleColumnChange = useCallback(
    (updated: { key: string; visible: boolean }[]) => {
      const next = new Set<string>();
      for (const { key, visible } of updated) {
        if (visible) next.add(key);
      }
      // "name" is always visible (required)
      next.add("name");
      setVisibleColumns(next);
    },
    [],
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

  // Derive all unique tags from loaded tasks (for the tag filter dropdown)
  const allTags = useMemo(() => {
    const tagSet = new Set<string>();
    for (const task of tasks) {
      for (const label of task.labels ?? []) {
        tagSet.add(label);
      }
    }
    return Array.from(tagSet).sort();
  }, [tasks]);

  // Sorted tasks (within each group, they'll be sorted by this)
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

  // Apply tag + CF filters on top of the sorted array
  const filteredSortedTasks = useMemo(
    () => applyViewFilters(sortedTasks, selectedTags, cfFilters),
    [sortedTasks, selectedTags, cfFilters],
  );

  // Group filtered+sorted tasks by status, ordered by status position
  const statusGroups = useMemo((): StatusGroup[] => {
    // Sort statuses by position
    const sortedStatuses = [...statuses].sort((a, b) => a.position - b.position);

    // Build a map from status_id -> tasks
    const tasksByStatus = new Map<string, Task[]>();
    for (const task of filteredSortedTasks) {
      // Only top-level tasks (no parent) in grouping
      if (task.parent_task_id) continue;
      const arr = tasksByStatus.get(task.status_id) ?? [];
      arr.push(task);
      tasksByStatus.set(task.status_id, arr);
    }

    // Build groups only for statuses that have tasks (or all statuses)
    const groups: StatusGroup[] = [];
    for (const status of sortedStatuses) {
      const groupTasks = tasksByStatus.get(status.id) ?? [];
      // Only include groups that have tasks
      if (groupTasks.length > 0) {
        groups.push({ status, tasks: groupTasks });
      }
    }

    // Also handle tasks with status_id not in statuses list (edge case)
    const knownStatusIds = new Set(sortedStatuses.map((s) => s.id));
    const unknownTasks = filteredSortedTasks.filter(
      (t) => !t.parent_task_id && !knownStatusIds.has(t.status_id),
    );
    if (unknownTasks.length > 0) {
      // Create a synthetic group
      groups.push({
        status: {
          id: "unknown",
          project_id: "",
          name: "Unknown",
          slug: "unknown",
          color: "#9ca3af",
          position: 9999,
          category: "backlog",
          is_default: false,
          auto_transition: {},
        },
        tasks: unknownTasks,
      });
    }

    return groups;
  }, [filteredSortedTasks, statuses]);

  // ---------------------------------------------------------------------------
  // Group collapse helpers
  // ---------------------------------------------------------------------------

  const toggleGroup = useCallback((statusId: string) => {
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(statusId)) {
        next.delete(statusId);
      } else {
        next.add(statusId);
      }
      return next;
    });
  }, []);

  // ---------------------------------------------------------------------------
  // Subtask expansion helpers
  // ---------------------------------------------------------------------------

  const toggleSubtasks = useCallback(
    async (taskId: string) => {
      let expanding = false;
      setExpandedTasks((prev) => {
        const next = new Set(prev);
        if (next.has(taskId)) {
          next.delete(taskId);
          return next;
        }
        next.add(taskId);
        expanding = true;
        return next;
      });

      // Always fetch fresh subtasks when expanding to avoid stale cache
      if (expanding) {
        setLoadingSubtasks((prev) => new Set(prev).add(taskId));
        try {
          const result = await api<{ items: Task[] }>(`/api/v1/tasks/${taskId}/subtasks`);
          setSubtaskMap((prev) => ({
            ...prev,
            [taskId]: result.items ?? [],
          }));
        } catch {
          toast.error("Failed to load subtasks");
          // Remove from expanded if failed
          setExpandedTasks((prev) => {
            const next = new Set(prev);
            next.delete(taskId);
            return next;
          });
        } finally {
          setLoadingSubtasks((prev) => {
            const next = new Set(prev);
            next.delete(taskId);
            return next;
          });
        }
      }
    },
    [],
  );

  // ---------------------------------------------------------------------------
  // Inline add task helpers
  // ---------------------------------------------------------------------------

  const startAddInGroup = useCallback((statusId: string) => {
    setAddingInGroup(statusId);
    setAddingTitle("");
  }, []);

  const cancelAdd = useCallback(() => {
    setAddingInGroup(null);
    setAddingTitle("");
  }, []);

  const commitAdd = useCallback(async () => {
    if (!addingTitle.trim() || !addingInGroup || !currentProject) return;
    const title = addingTitle.trim();
    const statusId = addingInGroup;
    setAddingInGroup(null);
    setAddingTitle("");
    try {
      await createTask(currentProject.id, {
        title,
        // Pass status_id via a cast — backend handles it
        ...(({ status_id: statusId } as unknown) as Record<string, unknown>),
      } as Parameters<typeof createTask>[1]);
      await fetchTasks(currentProject.id, { page, page_size: perPage });
      toast.success("Task created");
    } catch {
      toast.error("Failed to create task");
    }
  }, [addingTitle, addingInGroup, currentProject, createTask, fetchTasks, page, perPage]);

  // ---------------------------------------------------------------------------
  // Selection helpers
  // ---------------------------------------------------------------------------

  const allSelected =
    filteredSortedTasks.length > 0 &&
    filteredSortedTasks.filter((t) => !t.parent_task_id).every((t) => selectedTaskIds.has(t.id));
  const someSelected = selectedTaskIds.size > 0;

  const toggleSelectAll = useCallback(() => {
    if (allSelected) {
      setSelectedTaskIds(new Set());
    } else {
      setSelectedTaskIds(new Set(filteredSortedTasks.filter((t) => !t.parent_task_id).map((t) => t.id)));
    }
  }, [allSelected, filteredSortedTasks]);

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
      if (editingCell?.taskId === task.id) return;
      setSlideOverTaskId(task.id);
    },
    [editingCell],
  );

  // Sync current state to saved-view store
  const { pendingView, clearPendingView, setCurrentViewState } = useSavedViewStore();
  useEffect(() => {
    setCurrentViewState({ sortBy: sortField, sortOrder: sortDir });
  }, [sortField, sortDir, setCurrentViewState]);

  // Listen for saved view applied from ViewTabBar
  useEffect(() => {
    if (pendingView && pendingView.view_type === "list") {
      if (pendingView.sort_by) {
        setSortField(pendingView.sort_by as SortField);
      }
      if (pendingView.sort_order === "asc" || pendingView.sort_order === "desc") {
        setSortDir(pendingView.sort_order);
      }
      clearPendingView();
    }
  }, [pendingView, clearPendingView]);

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

  // Column count for colSpan calculations (checkbox + visible columns)
  const columnCount =
    1 + // checkbox (always visible)
    (visibleColumns.has("name") ? 1 : 0) +
    (visibleColumns.has("status") ? 1 : 0) +
    (visibleColumns.has("priority") ? 1 : 0) +
    (visibleColumns.has("assignee") ? 1 : 0) +
    (visibleColumns.has("due_date") ? 1 : 0) +
    (visibleColumns.has("labels") ? 1 : 0) +
    (visibleColumns.has("estimated_hours") ? 1 : 0) +
    (visibleColumns.has("created_at") ? 1 : 0) +
    sortedFieldDefs.filter((f) => visibleColumns.has(`cf:${f.slug}`)).length;

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
      {/* Header / Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        {/* Tag filter */}
        <TagFilterDropdown
          allTags={allTags}
          selectedTags={selectedTags}
          onChange={setSelectedTags}
        />

        {/* Custom field filter dialog */}
        <CustomFieldFilterDialog
          fields={sortedFieldDefs}
          filters={cfFilters}
          onChange={setCFFilters}
        />

        {/* Spacer */}
        <div className="flex-1" />

        {/* Column picker */}
        <ColumnPicker columns={allColumns} onChange={handleColumnChange} />
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
                  {visibleColumns.has("status") && (
                    <SortableHeader
                      label="Status"
                      field="status"
                      currentField={sortField}
                      dir={sortDir}
                      onSort={handleSort}
                      className="min-w-[120px]"
                    />
                  )}
                  {visibleColumns.has("priority") && (
                    <SortableHeader
                      label="Priority"
                      field="priority"
                      currentField={sortField}
                      dir={sortDir}
                      onSort={handleSort}
                      className="min-w-[100px]"
                    />
                  )}
                  {visibleColumns.has("assignee") && (
                    <SortableHeader
                      label="Assignee"
                      field="assignee"
                      currentField={sortField}
                      dir={sortDir}
                      onSort={handleSort}
                      className="min-w-[100px]"
                    />
                  )}
                  {visibleColumns.has("due_date") && (
                    <SortableHeader
                      label="Due Date"
                      field="due_date"
                      currentField={sortField}
                      dir={sortDir}
                      onSort={handleSort}
                      className="min-w-[110px]"
                    />
                  )}
                  {visibleColumns.has("labels") && (
                    <th className="px-3 py-2 text-xs font-medium text-muted-foreground">
                      Labels
                    </th>
                  )}
                  {visibleColumns.has("estimated_hours") && (
                    <th className="px-3 py-2 text-xs font-medium text-muted-foreground">
                      Estimate
                    </th>
                  )}
                  {visibleColumns.has("created_at") && (
                    <th className="px-3 py-2 text-xs font-medium text-muted-foreground">
                      Created
                    </th>
                  )}
                  {/* Dynamic custom field columns */}
                  {sortedFieldDefs
                    .filter((f) => visibleColumns.has(`cf:${f.slug}`))
                    .map((field) => (
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
                {statusGroups.map((group) => {
                  const isCollapsed = collapsedGroups.has(group.status.id);
                  const isAddingHere = addingInGroup === group.status.id;

                  return (
                    <>
                      {/* Group header row */}
                      <tr
                        key={`group-${group.status.id}`}
                        className="border-b border-border bg-muted/30 hover:bg-muted/50 transition-colors"
                      >
                        <td colSpan={columnCount} className="px-3 py-2">
                          <div className="flex items-center justify-between">
                            <button
                              type="button"
                              className="flex items-center gap-2 text-left"
                              onClick={() => toggleGroup(group.status.id)}
                              aria-expanded={!isCollapsed}
                            >
                              {isCollapsed ? (
                                <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
                              ) : (
                                <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                              )}
                              <span
                                className="inline-block h-2 w-2 rounded-full flex-shrink-0"
                                style={{ backgroundColor: group.status.color }}
                              />
                              <span className="text-xs font-semibold tracking-wide uppercase text-foreground/80">
                                {group.status.name}
                              </span>
                              <span className="text-xs text-muted-foreground font-normal">
                                ({group.tasks.length})
                              </span>
                            </button>
                            <button
                              type="button"
                              className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors px-2 py-0.5 rounded hover:bg-muted"
                              onClick={() => startAddInGroup(group.status.id)}
                            >
                              <Plus className="h-3 w-3" />
                              Add Task
                            </button>
                          </div>
                        </td>
                      </tr>

                      {/* Task rows for this group */}
                      {!isCollapsed && group.tasks.map((task) => {
                        const isSelected = selectedTaskIds.has(task.id);
                        const isExpanded = expandedTasks.has(task.id);
                        const isLoadingSubtask = loadingSubtasks.has(task.id);
                        const subtasks = subtaskMap[task.id] ?? [];

                        return (
                          <>
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
                              <EnhancedTitleCell
                                task={task}
                                statusColor={statusMap.get(task.status_id)?.color}
                                isEditing={
                                  editingCell?.taskId === task.id &&
                                  editingCell.field === "title"
                                }
                                isExpanded={isExpanded}
                                isLoadingSubtask={isLoadingSubtask}
                                onToggleSubtasks={() => toggleSubtasks(task.id)}
                                onStartEdit={() => startEdit(task.id, "title")}
                                onSave={(title) => saveCell(task.id, { title })}
                                onCancel={cancelEdit}
                                onNavigate={() => handleTaskClick(task)}
                              />

                              {/* Status */}
                              {visibleColumns.has("status") && (
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
                              )}

                              {/* Priority */}
                              {visibleColumns.has("priority") && (
                                <EnhancedPriorityCell
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
                              )}

                              {/* Assignee */}
                              {visibleColumns.has("assignee") && (
                                <td className="px-3 py-2">
                                  <AssigneeAvatar
                                    name={task.assignee_name ?? undefined}
                                    type={task.assignee_type as "user" | "agent" | "unassigned"}
                                    size="sm"
                                  />
                                </td>
                              )}

                              {/* Due Date */}
                              {visibleColumns.has("due_date") && (
                                <DueDateCell
                                  task={task}
                                  statusCategory={statusMap.get(task.status_id)?.category}
                                  isEditing={
                                    editingCell?.taskId === task.id &&
                                    editingCell.field === "due_date"
                                  }
                                  onStartEdit={() => startEdit(task.id, "due_date")}
                                  onSave={(due_date) => saveCell(task.id, { due_date })}
                                  onCancel={cancelEdit}
                                />
                              )}

                              {/* Labels */}
                              {visibleColumns.has("labels") && (
                                <td className="px-3 py-2">
                                  <div className="flex flex-wrap gap-1">
                                    {(task.labels ?? []).slice(0, 3).map((label) => (
                                      <span
                                        key={label}
                                        className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground"
                                      >
                                        {label}
                                      </span>
                                    ))}
                                    {(task.labels ?? []).length > 3 && (
                                      <span className="text-[10px] text-muted-foreground">
                                        +{(task.labels ?? []).length - 3}
                                      </span>
                                    )}
                                  </div>
                                </td>
                              )}

                              {/* Estimated hours */}
                              {visibleColumns.has("estimated_hours") && (
                                <td className="px-3 py-2 text-xs text-muted-foreground">
                                  {task.estimated_hours != null
                                    ? `${task.estimated_hours}h`
                                    : "--"}
                                </td>
                              )}

                              {/* Created at */}
                              {visibleColumns.has("created_at") && (
                                <td className="px-3 py-2 text-xs text-muted-foreground">
                                  {formatDate(task.created_at)}
                                </td>
                              )}

                              {/* Custom field columns */}
                              {sortedFieldDefs
                                .filter((f) => visibleColumns.has(`cf:${f.slug}`))
                                .map((field) => (
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
                                          ...(task.custom_fields ?? {}),
                                          [field.slug]: value,
                                        },
                                      })
                                    }
                                    onCancel={cancelEdit}
                                  />
                                ))}
                            </tr>

                            {/* Subtask rows */}
                            {isExpanded && (
                              isLoadingSubtask ? (
                                <tr key={`${task.id}-subtask-loading`} className="border-b border-border">
                                  <td colSpan={columnCount} className="px-3 py-2 pl-12">
                                    <div className="flex items-center gap-2 text-xs text-muted-foreground">
                                      <Loader2 className="h-3 w-3 animate-spin" />
                                      Loading subtasks...
                                    </div>
                                  </td>
                                </tr>
                              ) : subtasks.length === 0 ? (
                                <tr key={`${task.id}-subtask-empty`} className="border-b border-border">
                                  <td colSpan={columnCount} className="px-3 py-2 pl-12">
                                    <span className="text-xs text-muted-foreground">No subtasks</span>
                                  </td>
                                </tr>
                              ) : subtasks.map((subtask) => {
                                const subtaskStatus = statusMap.get(subtask.status_id);
                                return (
                                  <tr
                                    key={`subtask-${subtask.id}`}
                                    className="border-b border-border transition-colors hover:bg-muted/20 opacity-90"
                                  >
                                    {/* Checkbox (subtask) */}
                                    <td className="w-10 px-3 py-1.5" onClick={(e) => e.stopPropagation()}>
                                      {/* No checkbox for subtasks */}
                                    </td>

                                    {/* Subtask title */}
                                    <td
                                      className="cursor-pointer px-3 py-1.5 pl-10"
                                      onClick={() => handleTaskClick(subtask)}
                                    >
                                      <div className="flex items-center gap-1.5">
                                        <span className="text-muted-foreground/50 text-xs mr-0.5">└</span>
                                        {subtaskStatus && (
                                          <span
                                            className="inline-block h-1.5 w-1.5 rounded-full flex-shrink-0"
                                            style={{ backgroundColor: subtaskStatus.color }}
                                          />
                                        )}
                                        <span className="text-xs text-muted-foreground hover:text-foreground transition-colors">
                                          {subtask.title}
                                        </span>
                                      </div>
                                    </td>

                                    {/* Subtask status */}
                                    {visibleColumns.has("status") && (
                                      <td className="px-3 py-1.5">
                                        {subtaskStatus && (
                                          <div className="flex items-center gap-1.5">
                                            <span
                                              className="inline-block h-2 w-2 rounded-full"
                                              style={{ backgroundColor: subtaskStatus.color }}
                                            />
                                            <span className="text-xs text-muted-foreground">{subtaskStatus.name}</span>
                                          </div>
                                        )}
                                      </td>
                                    )}

                                    {/* Subtask priority */}
                                    {visibleColumns.has("priority") && (
                                      <td className="px-3 py-1.5">
                                        <PriorityFlag priority={subtask.priority} size="sm" />
                                      </td>
                                    )}

                                    {/* Subtask assignee */}
                                    {visibleColumns.has("assignee") && (
                                      <td className="px-3 py-1.5">
                                        <AssigneeAvatar
                                          name={subtask.assignee_name ?? undefined}
                                          type={subtask.assignee_type as "user" | "agent" | "unassigned"}
                                          size="sm"
                                        />
                                      </td>
                                    )}

                                    {/* Subtask due date */}
                                    {visibleColumns.has("due_date") && (
                                      <td className="px-3 py-1.5">
                                        <span className={cn("text-xs", getDateClass(subtask.due_date, subtaskStatus?.category))}>
                                          {subtask.due_date ? formatDate(subtask.due_date) : "--"}
                                        </span>
                                      </td>
                                    )}

                                    {/* Subtask labels */}
                                    {visibleColumns.has("labels") && (
                                      <td className="px-3 py-1.5">
                                        <span className="text-xs text-muted-foreground/50">--</span>
                                      </td>
                                    )}

                                    {/* Subtask estimated_hours */}
                                    {visibleColumns.has("estimated_hours") && (
                                      <td className="px-3 py-1.5">
                                        <span className="text-xs text-muted-foreground/50">--</span>
                                      </td>
                                    )}

                                    {/* Subtask created_at */}
                                    {visibleColumns.has("created_at") && (
                                      <td className="px-3 py-1.5">
                                        <span className="text-xs text-muted-foreground/50">
                                          {formatDate(subtask.created_at)}
                                        </span>
                                      </td>
                                    )}

                                    {/* Custom field placeholders for subtasks */}
                                    {sortedFieldDefs
                                      .filter((f) => visibleColumns.has(`cf:${f.slug}`))
                                      .map((field) => (
                                        <td key={field.id} className="px-3 py-1.5">
                                          <span className="text-xs text-muted-foreground/50">--</span>
                                        </td>
                                      ))}
                                  </tr>
                                );
                              })
                            )}
                          </>
                        );
                      })}

                      {/* Inline add task row */}
                      {!isCollapsed && isAddingHere && (
                        <tr key={`add-${group.status.id}`} className="border-b border-border bg-muted/10">
                          <td className="w-10 px-3 py-2" />
                          <td colSpan={columnCount - 1} className="px-3 py-2">
                            <div className="flex items-center gap-2">
                              <span
                                className="inline-block h-2 w-2 rounded-full flex-shrink-0"
                                style={{ backgroundColor: group.status.color }}
                              />
                              <Input
                                ref={addInputRef}
                                value={addingTitle}
                                onChange={(e) => setAddingTitle(e.target.value)}
                                placeholder="Task title..."
                                className="h-7 text-xs flex-1"
                                onKeyDown={(e) => {
                                  if (e.key === "Enter") {
                                    e.preventDefault();
                                    void commitAdd();
                                  }
                                  if (e.key === "Escape") {
                                    cancelAdd();
                                  }
                                }}
                                onBlur={() => {
                                  if (!addingTitle.trim()) {
                                    cancelAdd();
                                  }
                                }}
                              />
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 px-2 text-xs"
                                onClick={cancelAdd}
                              >
                                <X className="h-3.5 w-3.5" />
                              </Button>
                            </div>
                          </td>
                        </tr>
                      )}

                      {/* "Add Task" footer row per group (always visible when not collapsed) */}
                      {!isCollapsed && !isAddingHere && (
                        <tr key={`add-footer-${group.status.id}`} className="border-b border-border">
                          <td className="w-10 px-3 py-1.5" />
                          <td
                            colSpan={columnCount - 1}
                            className="px-3 py-1.5"
                          >
                            <button
                              type="button"
                              className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
                              onClick={() => startAddInGroup(group.status.id)}
                            >
                              <Plus className="h-3 w-3" />
                              Add Task
                            </button>
                          </td>
                        </tr>
                      )}
                    </>
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
                  <ChevronRight className="mr-0.5 h-3.5 w-3.5 rotate-180" />
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

      {/* Task slide-over */}
      <TaskSlideOver
        taskId={slideOverTaskId}
        onClose={() => setSlideOverTaskId(null)}
        onTaskUpdated={() => {
          if (currentProject) {
            fetchTasks(currentProject.id, { page, page_size: perPage });
          }
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Helper: compute due date CSS class
// ---------------------------------------------------------------------------

function getDateClass(
  dueDate: string | null | undefined,
  statusCategory: string | undefined,
): string {
  if (!dueDate) return "text-muted-foreground";
  const isTerminal = statusCategory === "done" || statusCategory === "cancelled";
  if (!isTerminal && new Date(dueDate) < new Date()) {
    return "text-red-500 font-medium";
  }
  return "text-muted-foreground";
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

// Enhanced title cell with visual indicators and subtask expansion
function EnhancedTitleCell({
  task,
  statusColor,
  isEditing,
  isExpanded,
  isLoadingSubtask,
  onToggleSubtasks,
  onStartEdit,
  onSave,
  onCancel,
  onNavigate,
}: {
  task: Task;
  statusColor?: string;
  isEditing: boolean;
  isExpanded: boolean;
  isLoadingSubtask: boolean;
  onToggleSubtasks: () => void;
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

  const hasSubtasks = (task.subtask_count ?? 0) > 0;
  const hasDescription = !!task.description;
  const artifactCount = task.artifact_count ?? 0;
  const subtaskCount = task.subtask_count ?? 0;

  return (
    <td
      className="cursor-pointer px-3 py-2"
      onDoubleClick={(e) => {
        e.stopPropagation();
        onStartEdit();
      }}
      onClick={onNavigate}
    >
      <div className="flex items-center gap-1.5">
        {/* Subtask expand toggle */}
        {hasSubtasks ? (
          <button
            type="button"
            className="flex-shrink-0 p-0.5 rounded hover:bg-muted transition-colors"
            onClick={(e) => {
              e.stopPropagation();
              onToggleSubtasks();
            }}
            aria-label={isExpanded ? "Collapse subtasks" : "Expand subtasks"}
          >
            {isLoadingSubtask ? (
              <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" />
            ) : isExpanded ? (
              <ChevronDown className="h-3 w-3 text-muted-foreground" />
            ) : (
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
            )}
          </button>
        ) : (
          <span className="w-4 flex-shrink-0" />
        )}

        {/* Status color dot */}
        {statusColor && (
          <span
            className="inline-block h-2 w-2 rounded-full flex-shrink-0"
            style={{ backgroundColor: statusColor }}
          />
        )}

        {/* Title */}
        <span className="font-medium hover:text-primary truncate">
          {task.title}
        </span>

        {/* Visual indicators */}
        <span className="flex items-center gap-1 ml-1 flex-shrink-0">
          {hasDescription && (
            <AlignLeft
              className="h-3 w-3 text-muted-foreground/60"
              aria-label="Has description"
            />
          )}
          {artifactCount > 0 && (
            <span className="flex items-center gap-0.5 text-muted-foreground/60">
              <Paperclip className="h-3 w-3" aria-hidden="true" />
              <span className="text-[10px]">{artifactCount}</span>
            </span>
          )}
          {subtaskCount > 0 && (
            <span className="flex items-center gap-0.5 text-muted-foreground/60">
              <GitBranch className="h-3 w-3" aria-hidden="true" />
              <span className="text-[10px]">{subtaskCount}</span>
            </span>
          )}
        </span>
      </div>
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
  statusMap: Map<string, TaskStatus>;
  statuses: TaskStatus[];
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

// Enhanced priority cell using PriorityFlag
function EnhancedPriorityCell({
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
      <PriorityFlag priority={task.priority} size="sm" />
    </td>
  );
}

// Due date cell with overdue styling
function DueDateCell({
  task,
  statusCategory,
  isEditing,
  onStartEdit,
  onSave,
  onCancel,
}: {
  task: Task;
  statusCategory?: string;
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

  const dateClass = getDateClass(task.due_date, statusCategory);

  return (
    <td
      className="cursor-pointer px-3 py-2"
      onClick={(e) => {
        e.stopPropagation();
        onStartEdit();
      }}
    >
      <span className={cn("text-xs", dateClass)}>
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
