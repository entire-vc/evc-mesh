import { useTranslation } from "react-i18next";
import { SlotBadges } from "@/components/extensions/slots/slot-host";
import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useParams, useSearchParams, useNavigate } from "react-router";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  ChevronDown,
  ChevronRight,
  List,
  Loader2,
  Lock,
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
import { CoreActionsProvider, useCan } from "@/core/primitives";
import { quickEditRequest, taskPath } from "@/core/primitives/actions";
import type { QuickEditField } from "@/core/primitives";
import { buildRowColumns } from "@/core/slots/task-row-data";
import { toStatusData, toTaskData } from "@/core/slots/board-data";
import { resolveSlotView, useSlotRegistryVersion, useThemeSettings, withSlotFallback } from "@/core/slots/registry";
import type { QuickEditPatch, SlotContext, StatusData, TaskRowColumn, TaskRowData, TaskRowSettings, TaskRowView } from "@/core/slots/types";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { useWorkspaceStore } from "@/stores/workspace";
import { useIsMobile } from "@/lib/use-is-mobile";
import i18n from "@/i18n";
import { cn } from "@/lib/cn";
import { api } from "@/lib/api";
import type {
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

// Labels resolve at render: t(`priority.${value}`) in the "list" namespace.
const PRIORITY_OPTIONS: Priority[] = ["urgent", "high", "medium", "low", "none"];

registerMeshDefaultTheme();

const DEFAULT_PER_PAGE = 50;

// Table sorts the server can apply before paging (task_repo.go ListByProject's
// allowed sort columns). The page has to be cut in the order the table shows:
// the server's own default is updated_at DESC, so a Title A→Z page 1 used to be
// the 50 most recently updated tasks sorted among themselves. The other sorts
// (status, assignee, custom fields) are still applied to the loaded page only.
const SERVER_SORT_FIELDS: Partial<Record<SortField, string>> = {
  title: "title",
  due_date: "due_date",
};

// Sortable columns of the header and their min width; the others get a plain header.
const SORTABLE_CLASS: Record<string, string> = {
  title: "min-w-[240px]",
  status: "min-w-[120px]",
  priority: "min-w-[100px]",
  assignee: "min-w-[100px]",
  due_date: "min-w-[110px]",
};

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
  const { t } = useTranslation("list");
  const { projectSlug, wsSlug } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();
  const { tasks, isLoading, total, hasMore, fetchTasks, updateTask, moveTask, deleteTask, createTask } =
    useTaskStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();

  const [sortField, setSortField] = useState<SortField>("title");
  const [sortDir, setSortDir] = useState<SortDir>("asc");

  // Slide-over state
  const [slideOverTaskId, setSlideOverTaskId] = useState<string | null>(null);

  // Selection state
  const [selectedTaskIds, setSelectedTaskIds] = useState<Set<string>>(new Set());

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

  // "Waiting on me" filter: server-side human_gate=true (subtask #3's param).
  // Unlike tags/CF, this must be a real query param rather than a client-side
  // filter over the loaded page — this view is paginated (DEFAULT_PER_PAGE=50),
  // so filtering only the current page would silently undercount against the
  // project's true human_gate=true set.
  const [humanGateOnly, setHumanGateOnly] = useState(false);

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

  // Shared fetch params for every fetchTasks call below, so the human_gate
  // filter and the server-side sort stay in sync with pagination everywhere
  // tasks get re-fetched.
  const serverSortBy = SERVER_SORT_FIELDS[sortField];
  const fetchParams = useMemo(
    () => ({
      page,
      page_size: perPage,
      ...(humanGateOnly ? { human_gate: "true" } : {}),
      ...(serverSortBy ? { sort_by: serverSortBy, sort_dir: sortDir } : {}),
    }),
    [page, perPage, humanGateOnly, serverSortBy, sortDir],
  );

  // The rows are laid out from statuses (grouping) and custom-field definitions
  // (columns). If the task list lands before either, the table is built and then
  // rebuilt, and how many DOM mutations that costs depends on which GET answered
  // first. So the skeleton stays up until both have answered once for this
  // project; later refetches (pagination, filter) do not blank the table.
  const [metaReadyFor, setMetaReadyFor] = useState<string | null>(null);
  const projectId = currentProject?.id;
  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    Promise.allSettled([
      fetchStatuses(projectId),
      // Custom fields API may not be available yet
      fetchCustomFields(projectId).catch(() => {}),
    ]).then(() => {
      if (!cancelled) setMetaReadyFor(projectId);
    });
    return () => {
      cancelled = true;
    };
  }, [projectId, fetchStatuses, fetchCustomFields]);

  useEffect(() => {
    if (currentProject) {
      fetchTasks(currentProject.id, fetchParams);
    }
  }, [currentProject, fetchTasks, fetchParams]);

  // Toggling the filter changes which page-1 items are "correct" for the
  // current URL page number — jump back to page 1 so the user doesn't land
  // on a now-out-of-range or mismatched page.
  const handleHumanGateOnlyChange = useCallback(
    (next: boolean) => {
      setHumanGateOnly(next);
      if (page !== 1) {
        const params = new URLSearchParams(searchParams);
        params.set("page", "1");
        setSearchParams(params);
      }
    },
    [page, searchParams, setSearchParams],
  );

  // Clear selection when tasks change (page navigation)
  useEffect(() => {
    setSelectedTaskIds(new Set());
  }, [page]);

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

  // task.row slot: the active theme's row view and settings; the core builds the columns it draws.
  const registryVersion = useSlotRegistryVersion();
  const themeSettings = useThemeSettings() as TaskRowSettings;
  const RowView = useMemo(
    () => withSlotFallback("task.row", resolveSlotView("task.row")) as TaskRowView,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [registryVersion],
  );
  const isMobile = useIsMobile();
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);
  const mode = document.documentElement.classList.contains("dark") ? "dark" : "light";
  const locale = i18n.language;
  const slotCtx = useMemo<SlotContext>(
    () => ({
      slot: "task.row",
      contractVersion: "1.0.0",
      locale,
      mode,
      isMobile,
      workspace: currentWorkspace
        ? { id: currentWorkspace.id, slug: currentWorkspace.slug, name: currentWorkspace.name }
        : null,
    }),
    [locale, mode, isMobile, currentWorkspace],
  );
  const canEdit = useCan("task.edit");
  const canMove = useCan("task.move");
  const rowColumns = useMemo(
    () => buildRowColumns(visibleColumns, sortedFieldDefs, (k) => t(k), themeSettings.row_fields),
    [visibleColumns, sortedFieldDefs, t, themeSettings.row_fields],
  );
  const statusDataList = useMemo(() => statuses.map(toStatusData), [statuses]);

  // Build allColumns from static defs + dynamic custom fields
  const allColumns = useMemo((): ColumnDef[] => {
    const staticCols: ColumnDef[] = [
      { key: "name", label: t("columns.name"), visible: true, required: true },
      { key: "status", label: t("columns.status"), visible: visibleColumns.has("status") },
      { key: "priority", label: t("columns.priority"), visible: visibleColumns.has("priority") },
      { key: "assignee", label: t("columns.assignee"), visible: visibleColumns.has("assignee") },
      { key: "due_date", label: t("columns.dueDate"), visible: visibleColumns.has("due_date") },
      { key: "labels", label: t("columns.labels"), visible: visibleColumns.has("labels") },
      {
        key: "estimated_hours",
        label: t("columns.estimate"),
        visible: visibleColumns.has("estimated_hours"),
      },
      { key: "created_at", label: t("columns.created"), visible: visibleColumns.has("created_at") },
    ];
    const cfCols: ColumnDef[] = sortedFieldDefs.map((f) => ({
      key: `cf:${f.slug}`,
      label: f.name,
      visible: visibleColumns.has(`cf:${f.slug}`),
    }));
    return [...staticCols, ...cfCols];
  }, [sortedFieldDefs, visibleColumns, t]);

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
      // A server-side sort re-cuts the pages: page N of the old order is not
      // page N of the new one, so start again from the top.
      if (page !== 1 && (SERVER_SORT_FIELDS[field] || SERVER_SORT_FIELDS[sortField])) {
        const params = new URLSearchParams(searchParams);
        params.set("page", "1");
        setSearchParams(params);
      }
    },
    [sortField, page, searchParams, setSearchParams],
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
          name: t("status.unknown"),
          slug: "unknown",
          color: "var(--fallback-gray-400)",
          position: 9999,
          category: "backlog",
          is_default: false,
          auto_transition: {},
        },
        tasks: unknownTasks,
      });
    }

    return groups;
  }, [filteredSortedTasks, statuses, t]);

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
      setExpandedTasks((prev) => {
        const next = new Set(prev);
        if (next.has(taskId)) {
          next.delete(taskId);
          return next;
        }
        next.add(taskId);
        return next;
      });

      // Fetch subtasks if not yet loaded
      if (!subtaskMap[taskId]) {
        setLoadingSubtasks((prev) => new Set(prev).add(taskId));
        try {
          const result = await api<{ items: Task[] }>(`/api/v1/tasks/${taskId}/subtasks`);
          setSubtaskMap((prev) => ({
            ...prev,
            [taskId]: result.items ?? [],
          }));
        } catch {
          toast.error(t("toast.loadSubtasksFailed"));
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
    [subtaskMap, t],
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
      await fetchTasks(currentProject.id, fetchParams);
      toast.success(t("toast.taskCreated"));
    } catch {
      toast.error(t("toast.createFailed"));
    }
  }, [addingTitle, addingInGroup, currentProject, createTask, fetchTasks, fetchParams, t]);

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

  const saveCell = useCallback(
    async (taskId: string, req: UpdateTaskRequest) => {
      try {
        await updateTask(taskId, req);
        toast.success(t("toast.taskUpdated"));
      } catch {
        toast.error(t("toast.updateFailed"));
      }
    },
    [updateTask, t],
  );

  // ---------------------------------------------------------------------------
  // Bulk action helpers
  // ---------------------------------------------------------------------------

  const runBulkUpdate = useCallback(
    async (updates: UpdateTaskRequest) => {
      const ids = Array.from(selectedTaskIds);
      setBulkProgress({ done: 0, total: ids.length });

      // Status changes go through moveTask (PATCH update ignores status_id).
      const isStatusChange = !!updates.status_id;
      const { status_id: statusId, ...fieldUpdates } = updates;

      let done = 0;
      const results = await Promise.allSettled(
        ids.map(async (id) => {
          if (isStatusChange && statusId) {
            await moveTask(id, { status_id: statusId });
          }
          // Apply non-status field updates if any.
          if (Object.keys(fieldUpdates).length > 0) {
            await updateTask(id, fieldUpdates);
          }
          done += 1;
          setBulkProgress({ done, total: ids.length });
        }),
      );

      const failed = results.filter((r) => r.status === "rejected").length;
      setBulkProgress(null);
      setSelectedTaskIds(new Set());

      if (failed === 0) {
        toast.success(t("toast.bulkUpdated", { count: ids.length }));
      } else {
        toast.error(t("toast.bulkUpdateFailed", { count: failed }));
      }

      if (currentProject) {
        await fetchTasks(currentProject.id, fetchParams);
      }
    },
    [selectedTaskIds, updateTask, moveTask, currentProject, fetchTasks, fetchParams, t],
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
      toast.success(t("toast.bulkDeleted", { count: ids.length }));
    } else {
      toast.error(t("toast.bulkDeleteFailed", { count: failed }));
    }

    if (currentProject) {
      await fetchTasks(currentProject.id, fetchParams);
    }
  }, [selectedTaskIds, deleteTask, currentProject, fetchTasks, fetchParams, t]);

  // ---------------------------------------------------------------------------
  // Navigation
  // ---------------------------------------------------------------------------

  const openTaskById = useCallback(
    (taskId: string) => {
    // Full-screen task page on every viewport. Was gated to isMobile in #70,
    // leaving desktop on the slide-over (Alex 2026-07-07 — fullscreen only).
      if (wsSlug && currentProject) {
        navigate(`/w/${wsSlug}/p/${currentProject.slug}/t/${taskId}`);
      } else {
        setSlideOverTaskId(taskId);
      }
    },
    [wsSlug, currentProject, navigate],
  );

  // What the TaskLink/InlineEdit primitives call: open the task, save one field.
  const taskHref = useCallback(
    (taskId: string) => taskPath(wsSlug, currentProject?.slug, taskId),
    [wsSlug, currentProject?.slug],
  );
  const coreActions = useMemo(
    () => ({
      openTask: openTaskById,
      taskHref,
      quickEdit: async (taskId: string, field: QuickEditField, value: unknown) => {
        await saveCell(taskId, quickEditRequest(field, value));
      },
    }),
    [openTaskById, taskHref, saveCell],
  );
  // Sync current state to saved-view store
  const { pendingView, clearPendingView, setCurrentViewState } = useSavedViewStore();
  useEffect(() => {
    setCurrentViewState({
      sortBy: sortField,
      sortOrder: sortDir,
      filters: { tags: selectedTags, custom_fields: cfFilters },
      columns: Array.from(visibleColumns),
    });
  }, [sortField, sortDir, selectedTags, cfFilters, visibleColumns, setCurrentViewState]);

  // Listen for saved view applied from ViewTabBar
  useEffect(() => {
    if (pendingView && pendingView.view_type === "list") {
      if (pendingView.sort_by) {
        setSortField(pendingView.sort_by as SortField);
      }
      if (pendingView.sort_order === "asc" || pendingView.sort_order === "desc") {
        setSortDir(pendingView.sort_order);
      }
      const filters = pendingView.filters ?? {};
      setSelectedTags(Array.isArray(filters.tags) ? (filters.tags as string[]) : []);
      setCFFilters((filters.custom_fields as CFFilters) ?? {});
      if (pendingView.columns) {
        // "name" is always visible (required, see handleColumnChange).
        setVisibleColumns(new Set([...pendingView.columns, "name"]));
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
  const columnCount = 1 + rowColumns.length;

  if (!currentProject) {
    return (
      <div className="flex items-center justify-center py-12">
        <p className="text-muted-foreground">
          {t("projectNotFound", { slug: projectSlug })}
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

        {/* Waiting on me (human_gate=true) filter */}
        <Button
          variant={humanGateOnly ? "default" : "outline"}
          size="sm"
          className="h-8 gap-1.5 px-2.5 text-xs"
          aria-pressed={humanGateOnly}
          onClick={() => handleHumanGateOnlyChange(!humanGateOnly)}
        >
          <Lock className="h-3.5 w-3.5" aria-hidden="true" />
          {t("toolbar.waitingOnMe")}
        </Button>

        {/* Spacer */}
        <div className="flex-1" />

        {/* Full create screen — alongside the per-group inline quick-add
            below, not instead of it. Quick-add stays for fast title-only
            capture; this is the entry point when the rest of the fields
            (description, assignee, labels, ...) matter. */}
        <Button
          size="sm"
          className="h-8 w-8 p-0"
          onClick={() => {
            if (wsSlug && currentProject) {
              navigate(`/w/${wsSlug}/p/${currentProject.slug}/new`);
            }
          }}
          title={t("toolbar.newTask")}
        >
          <Plus className="h-4 w-4" />
        </Button>

        {/* Column picker */}
        <ColumnPicker columns={allColumns} onChange={handleColumnChange} />
      </div>

      {/* Table */}
      {isLoading || metaReadyFor !== currentProject?.id ? (
        <div className="space-y-2">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full rounded-lg" />
          ))}
        </div>
      ) : tasks.length === 0 && page === 1 && humanGateOnly ? (
        <div className="flex flex-col items-center justify-center py-20">
          <Lock className="mb-4 h-12 w-12 text-muted-foreground" />
          <h3 className="mb-2 text-lg font-semibold">{t("empty.humanGateTitle")}</h3>
          <p className="mb-4 text-sm text-muted-foreground">
            {t("empty.humanGateDescription")}
          </p>
          <Button variant="outline" size="sm" onClick={() => handleHumanGateOnlyChange(false)}>
            {t("empty.clearFilter")}
          </Button>
        </div>
      ) : tasks.length === 0 && page === 1 ? (
        <div className="flex flex-col items-center justify-center py-20">
          <List className="mb-4 h-12 w-12 text-muted-foreground" />
          <h3 className="mb-2 text-lg font-semibold">{t("empty.noTasksTitle")}</h3>
          <p className="text-sm text-muted-foreground">
            {t("empty.noTasksDescription")}
          </p>
        </div>
      ) : (
        <>
          <CoreActionsProvider actions={coreActions}>
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
                      aria-label={t("table.selectAll")}
                    />
                  </th>
                  {rowColumns.map((column) => {
                    const sortable = SORTABLE_CLASS[column.key] ?? (column.customField ? "min-w-[100px]" : undefined);
                    return sortable ? (
                      <SortableHeader
                        key={column.key}
                        label={column.label}
                        field={column.customField ? column.customField.slug : column.key}
                        currentField={sortField}
                        dir={sortDir}
                        onSort={handleSort}
                        className={sortable}
                      />
                    ) : (
                      <th key={column.key} className="px-3 py-2 text-xs font-medium text-muted-foreground">
                        {column.label}
                      </th>
                    );
                  })}
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
                              {t("group.addTask")}
                            </button>
                          </div>
                        </td>
                      </tr>

                      {/* Task rows for this group */}
                      {!isCollapsed && group.tasks.map((task) => {
                        const isExpanded = expandedTasks.has(task.id);
                        const isLoadingSubtask = loadingSubtasks.has(task.id);
                        const subtasks = subtaskMap[task.id] ?? [];
                        const rowProps = {
                          View: RowView,
                          columns: rowColumns,
                          statuses: statusDataList,
                          fallbackStatus: group.status,
                          statusMap,
                          themeSettings,
                          ctx: slotCtx,
                          canEdit,
                          canMove,
                          onOpen: openTaskById,
                          onSave: saveCell,
                        };

                        return (
                          <Fragment key={task.id}>
                            <ListTaskRow
                              {...rowProps}
                              task={task}
                              depth={0}
                              selected={selectedTaskIds.has(task.id)}
                              expandable={(task.subtask_count ?? 0) > 0}
                              expanded={isExpanded}
                              expanding={isLoadingSubtask}
                              onToggleSelected={toggleSelectTask}
                              onToggleExpanded={toggleSubtasks}
                            />

                            {/* Subtask rows */}
                            {isExpanded && (
                              isLoadingSubtask ? (
                                <tr key={`${task.id}-subtask-loading`} className="border-b border-border">
                                  <td colSpan={columnCount} className="px-3 py-2 pl-12">
                                    <div className="flex items-center gap-2 text-xs text-muted-foreground">
                                      <Loader2 className="h-3 w-3 animate-spin" />
                                      {t("subtasks.loading")}
                                    </div>
                                  </td>
                                </tr>
                              ) : subtasks.length === 0 ? (
                                <tr key={`${task.id}-subtask-empty`} className="border-b border-border">
                                  <td colSpan={columnCount} className="px-3 py-2 pl-12">
                                    <span className="text-xs text-muted-foreground">{t("subtasks.empty")}</span>
                                  </td>
                                </tr>
                              ) : subtasks.map((subtask) => (
                                <ListTaskRow
                                  key={`subtask-${subtask.id}`}
                                  {...rowProps}
                                  task={subtask}
                                  depth={1}
                                  selected={false}
                                  expandable={false}
                                  expanded={false}
                                  expanding={false}
                                  onToggleSelected={toggleSelectTask}
                                  onToggleExpanded={toggleSubtasks}
                                />
                              ))
                            )}
                          </Fragment>
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
                                placeholder={t("group.taskTitlePlaceholder")}
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
                              {t("group.addTask")}
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
          </CoreActionsProvider>

          {/* Pagination footer */}
          {total > 0 && (
            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span>
                {t("pagination.showing", { start: rangeStart, end: rangeEnd, count: total })}
                {sortedFieldDefs.length > 0 &&
                  ` | ${t("pagination.customFields", { count: sortedFieldDefs.length })}`}
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
                  {t("pagination.previous")}
                </Button>
                <span className="tabular-nums">
                  {t("pagination.pageOf", { page, total: totalPages })}
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={!hasMore && page >= totalPages}
                  onClick={() => goToPage(page + 1)}
                >
                  {t("pagination.next")}
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
            <DialogTitle>{t("deleteDialog.title")}</DialogTitle>
            <DialogDescription>
              {t("deleteDialog.description", { count: selectedTaskIds.size })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setDeleteConfirmOpen(false)}
            >
              {t("deleteDialog.cancel")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={runBulkDelete}
            >
              <Trash2 className="mr-1.5 h-3.5 w-3.5" />
              {t("deleteDialog.confirm", { count: selectedTaskIds.size })}
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
            fetchTasks(currentProject.id, fetchParams);
          }
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Helper: compute due date CSS class
// ---------------------------------------------------------------------------

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
  const { t } = useTranslation("list");
  return (
    <div className="fixed bottom-6 left-1/2 z-40 -translate-x-1/2">
      <div className="flex items-center gap-3 rounded-xl border border-border bg-card px-4 py-2.5 shadow-lg">
        {progress ? (
          <div className="flex items-center gap-2 text-sm">
            <Loader2 className="h-4 w-4 animate-spin text-primary" />
            <span>
              {t("bulk.updating", { done: progress.done, total: progress.total })}
            </span>
          </div>
        ) : (
          <>
            <span className="text-sm font-medium">
              {t("bulk.selected", { count: selectedCount })}
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
              <option value="">{t("bulk.moveTo")}</option>
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
              <option value="">{t("bulk.setPriority")}</option>
              {PRIORITY_OPTIONS.map((p) => (
                <option key={p} value={p}>
                  {t(`priority.${p}`)}
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
              {t("bulk.delete")}
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

// ---------------------------------------------------------------------------
// task.row container: one task -> the slot view's data and actions
// ---------------------------------------------------------------------------

interface ListTaskRowProps {
  View: TaskRowView;
  task: Task;
  columns: TaskRowColumn[];
  statuses: StatusData[];
  statusMap: Map<string, TaskStatus>;
  fallbackStatus: TaskStatus;
  themeSettings: TaskRowSettings;
  ctx: SlotContext;
  canEdit: boolean;
  canMove: boolean;
  depth: number;
  selected: boolean;
  expandable: boolean;
  expanded: boolean;
  expanding: boolean;
  onOpen: (taskId: string) => void;
  onSave: (taskId: string, req: UpdateTaskRequest) => Promise<void>;
  onToggleSelected: (taskId: string) => void;
  onToggleExpanded: (taskId: string) => void;
}

function ListTaskRow({
  View,
  task,
  columns,
  statuses,
  statusMap,
  fallbackStatus,
  themeSettings,
  ctx,
  canEdit,
  canMove,
  depth,
  selected,
  expandable,
  expanded,
  expanding,
  onOpen,
  onSave,
  onToggleSelected,
  onToggleExpanded,
}: ListTaskRowProps) {
  const Badges = useMemo(() => {
    const B = () => <SlotBadges task={task} />;
    return B;
  }, [task]);
  const statusRow = statusMap.get(task.status_id) ?? fallbackStatus;
  const data = useMemo<TaskRowData>(
    () => ({
      task: toTaskData(task, statusRow.category),
      status: toStatusData(statusRow),
      columns,
      selected,
      focused: false,
      depth,
      expandable,
      expanded,
      statuses,
      expanding,
      createdAt: task.created_at,
      components: { Badges },
    }),
    [task, statusRow, columns, selected, depth, expandable, expanded, statuses, expanding, Badges],
  );
  const actions = useMemo(
    () => ({
      openTask: (opts?: { newTab?: boolean }) => {
        if (opts?.newTab) window.open(taskPath(undefined, undefined, task.id), "_blank", "noopener");
        else onOpen(task.id);
      },
      toggleSelected: () => onToggleSelected(task.id),
      toggleExpanded: expandable ? () => onToggleExpanded(task.id) : undefined,
      quickEdit: canEdit
        ? async (patch: QuickEditPatch) => {
            const req = quickEditRequest(patch.field, patch.value);
            // A custom-field patch carries the whole map, as the list always sent it.
            if (patch.field.startsWith("cf:")) {
              req.custom_fields = { ...(task.custom_fields ?? {}), [patch.field.slice(3)]: patch.value };
            }
            await onSave(task.id, req);
          }
        : undefined,
      changeStatus: canMove ? (statusId: string) => onSave(task.id, { status_id: statusId }) : undefined,
    }),
    [task, expandable, canEdit, canMove, onOpen, onSave, onToggleSelected, onToggleExpanded],
  );
  return <View data={data} actions={actions} themeSettings={themeSettings} ctx={ctx} />;
}
