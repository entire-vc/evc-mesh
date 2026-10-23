import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useParams, useNavigate } from "react-router";
import { useShallow } from "zustand/react/shallow";
import { CoreActionsProvider, DndProvider, useCan, useCoreActions, useRealtime } from "@/core/primitives";
import type { MoveRequest, RealtimeEvent } from "@/core/primitives";
import { planMoveToIndex } from "@/lib/board-move";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { resolveSlotView, useSlotRegistryVersion, useThemeSettings, withSlotFallback } from "@/core/slots/registry";
import { boardCardMemoEnabled, resolveBoardComponents } from "@/core/slots/board-host";
import { BOARD_CONTRACT_VERSION } from "@/core/slots/types";
import type {
  Async,
  BoardColumnData,
  BoardLayoutActions,
  BoardSortBy,
  BoardLayoutSettings,
  QuickEditPatch,
  SlotContext,
} from "@/core/slots/types";
import { useAgentShortTagMap } from "@/hooks/use-agent-short-tag";
import { useSlotOwnedVcsProviders } from "@/components/extensions/slots/slot-host";
import { buildFieldCatalog, resolveCardFields, stabilizeColumns, toStatusData, toTaskData } from "@/core/slots/board-data";
import { taskPath } from "@/core/primitives/actions";
import type { QuickEditField } from "@/core/primitives";
import { useProjectStore } from "@/stores/project";
import { cancelBoardRefresh, useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useMemberStore } from "@/stores/member";
import { useDeferredRefetch } from "@/hooks/use-deferred-refetch";
import { TaskSlideOver } from "@/components/task-slide-over";
import { toast } from "@/components/ui/toast";
import { BoardToolbar, type GroupBy, type SortBy } from "@/components/board-toolbar";
import { useSavedViewStore } from "@/stores/saved-view-store";
import { CreateRecurringDialog } from "@/components/create-recurring-dialog";
import { passesSubtaskFilter, relocateGatedToTriage } from "@/lib/board-gate-triage";
import { applyViewFilters, type CFFilters } from "@/components/view-filters";
import { loadBoardFilters, saveBoardFilters } from "@/lib/board-view-storage";
import { isClosedCategory, type ClosedSlice } from "@/lib/board-load";
import { isWorking } from "@/lib/runtime-state";
import { useIsMobile } from "@/lib/use-is-mobile";
import { useWorkspaceStore } from "@/stores/workspace";
import i18n from "@/i18n";
import {
  BOARD_FILTER_DEFAULTS,
  GROUP_BY_VALUES,
  SORT_BY_VALUES,
  buildBoardSavedViewState,
  readBoardSavedViewState,
} from "@/lib/board-saved-view";
import type { Task, TaskStatus, Priority, StatusCategory } from "@/types";

// ---------------------------------------------------------------------------
// Priority metadata (for GroupBy=priority columns)
// ---------------------------------------------------------------------------

registerMeshDefaultTheme();

const PRIORITY_ORDER: Record<Priority, number> = {
  urgent: 0,
  high: 1,
  medium: 2,
  low: 3,
  none: 4,
};

const PRIORITY_COLORS: Record<Priority, string> = {
  urgent: "var(--priority-fill-urgent)",
  high: "var(--priority-fill-high)",
  medium: "var(--priority-fill-medium)",
  low: "var(--priority-fill-low)",
  none: "var(--fallback-gray-400)",
};

// ---------------------------------------------------------------------------
// Generic column descriptor (works for all GroupBy modes)
// ---------------------------------------------------------------------------

interface BoardCol {
  id: string; // droppable column id prefix value
  title: string;
  color: string;
  // For status mode: the underlying TaskStatus
  status?: TaskStatus;
  // For priority mode: the Priority value
  priority?: Priority;
  // For assignee mode: assignee_id or "unassigned"
  assigneeId?: string;
  assigneeName?: string;
  assigneeType?: "user" | "agent" | "unassigned";
}

// ---------------------------------------------------------------------------
// Closed columns (Board, status grouping): only a slice is loaded
// ---------------------------------------------------------------------------

/** What a closed column knows about the cards it does not hold. */
export interface ClosedColumnInfo {
  slice: ClosedSlice;
  /** Any board filter is on: the server total no longer counts what is shown. */
  filtersActive: boolean;
  /** Next page of this status; rejects on failure. */
  onLoadMore: (statusId: string) => Promise<void>;
}

/**
 * Header count for a column. A closed column whose slice is partial shows the
 * server's total when no filter is on — that IS the number of cards in it.
 * With a filter on, the server cannot answer (the subtask, tag and custom-field
 * filters have no API equivalent), so it shows what matched among the loaded
 * cards with a "+": at least that many, maybe more in the unloaded rest.
 */
export function columnCountLabel(visible: number, closed: ClosedColumnInfo | undefined): string {
  if (!closed || !closed.slice.hasMore) return String(visible);
  return closed.filtersActive ? `${visible}+` : String(closed.slice.total);
}

/** True when both arrays hold the SAME task objects, in the same order —
 * cheaper than deep-comparing task content, and enough: moveTask +
 * groupByStatus (stores/task.ts) always give an unrelated task back its old
 * object reference, so this is exact, not an approximation. */
export function tasksArrayEqual(a: Task[], b: Task[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

/** True when both maps hold the same key → value pairs. holderNameById is
 * rebuilt (new Map, same content) on every re-render whose deps include
 * `tasks`, so a reference comparison would defeat the column memo on every
 * drag. Member/holder counts are small (tens, not thousands): this is cheap. */
export function holderMapsEqual(a: Map<string, string>, b: Map<string, string>): boolean {
  if (a === b) return true;
  if (a.size !== b.size) return false;
  for (const [k, v] of a) {
    if (b.get(k) !== v) return false;
  }
  return true;
}

export { boardCardMemoEnabled };

// ---------------------------------------------------------------------------
// Sort helper — sort tasks within a column by the given SortBy value
// ---------------------------------------------------------------------------

const NO_GATE_STATUS: Record<string, string> = {};

function sortTasks(tasks: Task[], sortBy: SortBy): Task[] {
  if (sortBy === "manual") {
    return [...tasks].sort((a, b) => a.position - b.position);
  }
  return [...tasks].sort((a, b) => {
    switch (sortBy) {
      case "priority":
        return PRIORITY_ORDER[a.priority] - PRIORITY_ORDER[b.priority];
      case "due_date": {
        const da = a.due_date ? new Date(a.due_date).getTime() : Infinity;
        const db = b.due_date ? new Date(b.due_date).getTime() : Infinity;
        return da - db;
      }
      case "updated":
        return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();
      case "created":
        return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
      case "title":
        return a.title.localeCompare(b.title);
      default:
        return a.position - b.position;
    }
  });
}

// ---------------------------------------------------------------------------
// Board page
// ---------------------------------------------------------------------------

export function BoardPage() {
  const { t } = useTranslation("board");
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  // useShallow, not a bare store call: each store here carries fields this
  // page never reads (total/page/… on the task store, for instance),
  // and a bare `useStore()` re-renders on ANY of them changing, not just the
  // ones destructured below. A drag's own moveTask touches several of those
  // unrelated fields in the same `set()` — without this, every column and
  // card would re-render on state a drag doesn't even use.
  const { currentProject, statuses, fetchStatuses } = useProjectStore(
    useShallow((s) => ({
      currentProject: s.currentProject,
      statuses: s.statuses,
      fetchStatuses: s.fetchStatuses,
    })),
  );
  const {
    tasks, tasksByStatus, isLoading, listProjectId, listComplete, listError,
    closedSlices, fetchBoardTasks, loadMoreClosed, moveTask,
  } = useTaskStore(
    useShallow((s) => ({
      tasks: s.tasks,
      tasksByStatus: s.tasksByStatus,
      isLoading: s.isLoading,
      listProjectId: s.listProjectId,
      listComplete: s.listComplete,
      listError: s.listError,
      closedSlices: s.closedSlices,
      fetchBoardTasks: s.fetchBoardTasks,
      loadMoreClosed: s.loadMoreClosed,
      moveTask: s.moveTask,
    })),
  );
  // Open work is loaded in full, closed statuses as a newest-first slice
  // (lib/board-load.ts). Statuses are read at call time: the load needs to
  // know which status ids are open and which closed.
  const fetchTasks = useCallback(
    (projectId: string, opts?: { background?: boolean; statusesLoaded?: Promise<void> }) => {
      const ofProject = () =>
        useProjectStore.getState().statuses.filter((s) => !s.project_id || s.project_id === projectId);
      return fetchBoardTasks(
        projectId,
        opts?.statusesLoaded
          ? opts.statusesLoaded.then(() => {
              const list = ofProject();
              // fetchStatuses swallows its error into an empty list; without
              // this the board would show an empty, "complete" project.
              const failed = useProjectStore.getState().error;
              if (!list.length && failed) throw new Error(failed);
              return list;
            })
          : ofProject,
        opts,
      );
    },
    [fetchBoardTasks],
  );
  // Reload statuses with the tasks: a failed statuses request is a reason to retry.
  const reloadBoard = useCallback(
    (projectId: string) => fetchTasks(projectId, { statusesLoaded: fetchStatuses(projectId) }),
    [fetchTasks, fetchStatuses],
  );
  useEffect(() => cancelBoardRefresh, []);
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } = useCustomFieldStore(
    useShallow((s) => ({ fields: s.fields, fetchFields: s.fetchFields })),
  );
  const { projectMembers, fetchProjectMembers } = useMemberStore(
    useShallow((s) => ({
      projectMembers: s.projectMembers,
      fetchProjectMembers: s.fetchProjectMembers,
    })),
  );

  // Slide-over state
  const [slideOverTaskId, setSlideOverTaskId] = useState<string | null>(null);

  // Drag state

  // ---- Toolbar state ----
  const [groupBy, setGroupBy] = useState<GroupBy>("status");
  const [sortBy, setSortBy] = useState<SortBy>("updated");
  const [showClosed, setShowClosed] = useState(false);
  const [showSubtasks, setShowSubtasks] = useState(false);
  const [humanGateOnly, setHumanGateOnly] = useState(false);
  const [workingOnly, setWorkingOnly] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [priorityFilter, setPriorityFilter] = useState<string>("all");
  const [assigneeFilter, setAssigneeFilter] = useState<string>("all");
  const [customFieldFilters, setCustomFieldFilters] = useState<Record<string, unknown>>({});
  // New filter state
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [cfFilters, setCFFilters] = useState<CFFilters>({});
  // Specific-assignee filter: task.assignee_id (or "unassigned") must be in this
  // set. Empty = no filtering. Distinct from assigneeFilter above, which filters
  // by assignee_type (user/agent/unassigned), not by who specifically.
  const [assigneeIdsFilter, setAssigneeIdsFilter] = useState<string[]>([]);

  // True once this project's persisted filters (or defaults, if none were
  // saved) have been applied. Gates the save effect below so it never
  // overwrites a project's saved filters with another project's stale state
  // during the brief window while switching projects.
  const [filtersHydrated, setFiltersHydrated] = useState(false);

  // A WS event refreshes in the background (no skeleton remount), debounced. While
  // a card is being dragged the refresh waits for the drop/cancel, so the lists do
  // not change under the pointer.
  const {
    schedule: debouncedRefetch,
    startDrag: markDragStart,
    flush: flushPendingRefetch,
  } = useDeferredRefetch(
    useCallback(() => {
      if (currentProject) fetchTasks(currentProject.id, { background: true });
    }, [currentProject, fetchTasks]),
  );

  // Listen for real-time WebSocket events.
  useRealtime(
    { kind: "project", id: currentProject?.id },
    useCallback(
      (event: RealtimeEvent) => {
        const eventType = event.type;
        if (eventType === "summary") {
          const data = event.data as Record<string, unknown>;
          const subject = (data.subject as string) || t("toast.agentSummary");
          toast(subject);
        }
        if (
          eventType === "status_change" ||
          eventType === "context_update" ||
          eventType === "dependency_resolved" ||
          eventType === "summary" ||
          eventType === "custom"
        ) {
          debouncedRefetch();
        }
      },
      [debouncedRefetch, t],
    ),
  );

  useEffect(() => {
    if (!currentProject) return;
    // Tasks are requested by status id (open in full, closed as a slice), so
    // the task requests wait for the statuses. A failed statuses fetch leaves
    // an empty list: the board then says "No statuses configured", as before.
    // A project switch supersedes this load inside the store (request id).
    void reloadBoard(currentProject.id);
    fetchCustomFields(currentProject.id).catch(() => {
      // Custom fields API may not be available yet
    });
    fetchProjectMembers(currentProject.id);
  }, [currentProject, reloadBoard, fetchCustomFields, fetchProjectMembers]);

  // Restore this project's persisted grouping/sort/filters (or defaults, if
  // this is the first visit) so a reload or navigating away and back doesn't
  // reset the board to its out-of-the-box view.
  const currentProjectId = currentProject?.id;
  useEffect(() => {
    if (!currentProjectId) return;
    setFiltersHydrated(false);
    const saved = loadBoardFilters(currentProjectId);
    if (saved) {
      if (GROUP_BY_VALUES.includes(saved.groupBy as GroupBy)) {
        setGroupBy(saved.groupBy as GroupBy);
      }
      if (SORT_BY_VALUES.includes(saved.sortBy as SortBy)) {
        setSortBy(saved.sortBy as SortBy);
      }
      setShowClosed(saved.showClosed ?? false);
      setShowSubtasks(saved.showSubtasks ?? false);
      setHumanGateOnly(saved.humanGateOnly ?? false);
      setSearchQuery(saved.searchQuery ?? "");
      setPriorityFilter(saved.priorityFilter ?? "all");
      setAssigneeFilter(saved.assigneeFilter ?? "all");
      setCustomFieldFilters(saved.customFieldFilters ?? {});
      setSelectedTags(saved.selectedTags ?? []);
      setCFFilters(saved.cfFilters ?? {});
      setAssigneeIdsFilter(saved.assigneeIdsFilter ?? []);
    }
    setFiltersHydrated(true);
  }, [currentProjectId]);

  // Persist grouping/sort/filters whenever they change, once this project's
  // saved state has been loaded (avoids clobbering it with defaults first).
  useEffect(() => {
    if (!currentProjectId || !filtersHydrated) return;
    saveBoardFilters(currentProjectId, {
      groupBy,
      sortBy,
      showClosed,
      showSubtasks,
      humanGateOnly,
      searchQuery,
      priorityFilter,
      assigneeFilter,
      customFieldFilters,
      selectedTags,
      cfFilters,
      assigneeIdsFilter,
    });
  }, [
    currentProjectId,
    filtersHydrated,
    groupBy,
    sortBy,
    showClosed,
    showSubtasks,
    humanGateOnly,
    searchQuery,
    priorityFilter,
    assigneeFilter,
    customFieldFilters,
    selectedTags,
    cfFilters,
    assigneeIdsFilter,
  ]);

  const sortedStatuses = useMemo(
    () => [...statuses].sort((a, b) => a.position - b.position),
    [statuses],
  );

  // All custom field defs passed to filter dialog
  const filterableFields = useMemo(
    () => [...customFieldDefs].sort((a, b) => a.position - b.position),
    [customFieldDefs],
  );

  // Derive all unique tags from loaded tasks
  const allTags = useMemo(() => {
    const tagSet = new Set<string>();
    for (const task of tasks) {
      for (const label of task.labels ?? []) {
        tagSet.add(label);
      }
    }
    return Array.from(tagSet).sort();
  }, [tasks]);

  // Candidates for the specific-assignee filter: project members (so people
  // with zero currently-loaded tasks are still pickable) merged with whoever
  // tasks are actually assigned to (covers a legacy assignee no longer a
  // formal member). Deduped by id, named assignees sorted first.
  const assigneeCandidates = useMemo(() => {
    const byId = new Map<string, { id: string; name: string; type: "user" | "agent" }>();
    for (const m of projectMembers) {
      if (m.user) {
        byId.set(m.user.id, { id: m.user.id, name: m.user.name || m.user.email, type: "user" });
      } else if (m.agent_id) {
        byId.set(m.agent_id, { id: m.agent_id, name: m.agent_name ?? t("fallback.agent"), type: "agent" });
      }
    }
    for (const task of tasks) {
      if (task.assignee_id && !byId.has(task.assignee_id)) {
        byId.set(task.assignee_id, {
          id: task.assignee_id,
          name: task.assignee_name ?? task.assignee_id,
          type: task.assignee_type === "agent" ? "agent" : "user",
        });
      }
    }
    return Array.from(byId.values()).sort((a, b) => a.name.localeCompare(b.name));
  }, [projectMembers, tasks, t]);

  // id → display name for checkout holders. Built from the same two sources as
  // assigneeCandidates: project members cover the normal case, and assignees seen
  // on loaded tasks cover a holder who is not a formal member of this project.
  const shortTags = useAgentShortTagMap();
  const ownedVcs = useSlotOwnedVcsProviders("task.badge");
  const holderNameById = useMemo(() => {
    const byId = new Map<string, string>();
    for (const m of projectMembers) {
      if (m.user) byId.set(m.user.id, m.user.name || m.user.email);
      else if (m.agent_id) byId.set(m.agent_id, m.agent_name ?? t("fallback.agent"));
    }
    for (const task of tasks) {
      if (task.assignee_id && task.assignee_name && !byId.has(task.assignee_id)) {
        byId.set(task.assignee_id, task.assignee_name);
      }
    }
    return byId;
  }, [projectMembers, tasks, t]);

  // ---------------------------------------------------------------------------
  // Base task filter (search + priority + assignee + tags + CF + subtasks)
  // Applied before grouping.
  // ---------------------------------------------------------------------------

  const closedStatusIds = useMemo(
    () => new Set(statuses.filter((s) => s.category === "done" || s.category === "cancelled").map((s) => s.id)),
    [statuses],
  );

  const filteredTasks = useMemo(() => {
    // First pass: basic filters (subtasks, search, priority, assignee)
    const basic = tasks.filter((task) => {
      if (!showClosed && closedStatusIds.has(task.status_id)) return false;
      if (!passesSubtaskFilter(task, showSubtasks, closedStatusIds)) return false;
      if (searchQuery && !task.title.toLowerCase().includes(searchQuery.toLowerCase())) {
        return false;
      }
      if (priorityFilter !== "all" && task.priority !== priorityFilter) return false;
      if (assigneeFilter !== "all" && task.assignee_type !== assigneeFilter) return false;
      if (
        assigneeIdsFilter.length > 0 &&
        !assigneeIdsFilter.includes(task.assignee_id ?? "unassigned")
      ) {
        return false;
      }
      if (humanGateOnly && !task.human_gate) return false;
      // The list endpoint returns the effective runtime_state (checkout fallback included), and the board holds the full list.
      if (workingOnly && !isWorking(task)) return false;
      return true;
    });

    // Second pass: tag + CF filters via shared pure function
    return applyViewFilters(basic, selectedTags, cfFilters);
  }, [
    tasks,
    showClosed,
    showSubtasks,
    closedStatusIds,
    searchQuery,
    priorityFilter,
    assigneeFilter,
    assigneeIdsFilter,
    humanGateOnly,
    workingOnly,
    selectedTags,
    cfFilters,
  ]);

  // Closed columns hold a slice; their header count needs to know whether any
  // filter narrows what is shown (columnCountLabel). Hiding subtasks counts:
  // closed subtasks are hidden then, and the server total includes them.
  const filtersActive =
    searchQuery !== "" ||
    priorityFilter !== "all" ||
    assigneeFilter !== "all" ||
    assigneeIdsFilter.length > 0 ||
    humanGateOnly ||
    workingOnly ||
    selectedTags.length > 0 ||
    Object.keys(cfFilters).length > 0 ||
    !showSubtasks;
  const projectId = currentProject?.id;
  const closedInfoByStatus = useMemo(() => {
    const out: Record<string, ClosedColumnInfo> = {};
    const onLoadMore = (statusId: string) => loadMoreClosed(projectId ?? "", statusId);
    for (const [id, slice] of Object.entries(closedSlices)) out[id] = { slice, filtersActive, onLoadMore };
    return out;
  }, [closedSlices, filtersActive, loadMoreClosed, projectId]);
  // Other groupings mix open and closed cards in one column; say once that the
  // closed part is a slice.
  const closedPartial = useMemo(() => {
    let loaded = 0, total = 0, partial = false;
    for (const slice of Object.values(closedSlices)) {
      loaded += slice.loaded;
      total += slice.total;
      partial ||= slice.hasMore;
    }
    return partial ? { loaded, total } : null;
  }, [closedSlices]);

  // ---------------------------------------------------------------------------
  // Build columns + task groups based on groupBy
  // ---------------------------------------------------------------------------

  const { columns: rawColumns, tasksByColumn: baseTasksByColumn } = useMemo((): {
    columns: BoardCol[];
    tasksByColumn: Record<string, Task[]>;
  } => {
    if (groupBy === "status") {
      // ------ Status grouping (original behavior) ------
      const closedCategories: StatusCategory[] = ["done", "cancelled"];

      const visibleStatuses = sortedStatuses.filter((s) => {
        if (!showClosed && closedCategories.includes(s.category)) return false;
        return true;
      });

      const cols: BoardCol[] = visibleStatuses.map((s) => ({
        id: s.id,
        title: s.name,
        color: s.color,
        status: s,
      }));

      const byCol: Record<string, Task[]> = {};
      const filteredIds = new Set(filteredTasks.map((task) => task.id));
      for (const col of cols) {
        const raw = tasksByStatus[col.id] ?? [];
        // Apply global filters
        const filtered = raw.filter((t) => filteredIds.has(t.id));
        byCol[col.id] = sortTasks(filtered, sortBy);
      }

      return { columns: cols, tasksByColumn: byCol };
    }

    if (groupBy === "priority") {
      // ------ Priority grouping ------
      const priorities: Priority[] = ["urgent", "high", "medium", "low", "none"];

      const cols: BoardCol[] = priorities.map((p) => ({
        id: `priority-${p}`,
        title: p === "none" ? t("priority.noPriority") : t(`priority.${p}`),
        color: PRIORITY_COLORS[p],
        priority: p,
      }));

      const byCol: Record<string, Task[]> = {};
      for (const col of cols) {
        const pri = col.priority!;
        const filtered = filteredTasks.filter((t) => t.priority === pri);
        byCol[col.id] = sortTasks(filtered, sortBy);
      }

      return { columns: cols, tasksByColumn: byCol };
    }

    // ------ Assignee grouping ------
    // Derive unique assignees from all filtered tasks
    const seenIds = new Set<string>();
    const assignees: Array<{
      id: string;
      name: string | null;
      type: "user" | "agent" | "unassigned";
    }> = [];

    for (const t of filteredTasks) {
      const key = t.assignee_id ?? "unassigned";
      if (!seenIds.has(key)) {
        seenIds.add(key);
        assignees.push({
          id: key,
          name: t.assignee_name ?? null,
          type:
            t.assignee_type === "user" || t.assignee_type === "agent"
              ? t.assignee_type
              : "unassigned",
        });
      }
    }

    // Sort: named assignees first, then unassigned
    assignees.sort((a, b) => {
      if (a.id === "unassigned") return 1;
      if (b.id === "unassigned") return -1;
      return (a.name ?? "").localeCompare(b.name ?? "");
    });

    // Ensure there's always an "Unassigned" column
    if (!seenIds.has("unassigned")) {
      assignees.push({ id: "unassigned", name: t("toolbar.assignee.unassigned"), type: "unassigned" });
    }

    const cols: BoardCol[] = assignees.map((a) => ({
      id: `assignee-${a.id}`,
      title: a.name ?? t("toolbar.assignee.unassigned"),
      color: "var(--fallback-gray-400)",
      assigneeId: a.id,
      assigneeName: a.name ?? undefined,
      assigneeType: a.type,
    }));

    const byCol: Record<string, Task[]> = {};
    for (const col of cols) {
      const aId = col.assigneeId!;
      const filtered = filteredTasks.filter(
        (t) => (t.assignee_id ?? "unassigned") === aId,
      );
      byCol[col.id] = sortTasks(filtered, sortBy);
    }

    return { columns: cols, tasksByColumn: byCol };
  }, [
    groupBy,
    sortBy,
    showClosed,
    sortedStatuses,
    tasksByStatus,
    filteredTasks,
    t,
  ]);

  // Cards waiting on a human are listed in the Triage column (once, with their
  // real status shown) so the human sees them without any status change.
  const { tasksByColumn, gateStatusByTask } = useMemo(() => {
    if (groupBy !== "status") return { tasksByColumn: baseTasksByColumn, gateStatusByTask: NO_GATE_STATUS };
    const r = relocateGatedToTriage(
      rawColumns,
      baseTasksByColumn,
      new Set(filteredTasks.map((t) => t.id)),
      (rows) => sortTasks(rows, sortBy),
    );
    return { tasksByColumn: r.tasksByColumn, gateStatusByTask: r.statusNameByTask };
  }, [groupBy, rawColumns, baseTasksByColumn, filteredTasks, sortBy]);

  // ----- Contract data for the board.* slots -----

  const canMove = useCan("task.move");
  const canCreate = useCan("task.create");
  const isMobile = useIsMobile();
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);
  const themeSettings = useThemeSettings() as BoardLayoutSettings;

  const fieldCatalog = useMemo(
    () => buildFieldCatalog(customFieldDefs, (k) => t(k)),
    [customFieldDefs, t],
  );
  const cardFields = useMemo(
    () => resolveCardFields(fieldCatalog, themeSettings.card_fields).map((f) => f.key),
    [fieldCatalog, themeSettings.card_fields],
  );

  const mode = document.documentElement.classList.contains("dark") ? "dark" : "light";
  const locale = i18n.language;
  const ctx = useMemo<SlotContext>(
    () => ({
      slot: "board.layout",
      contractVersion: BOARD_CONTRACT_VERSION,
      locale,
      mode,
      isMobile,
      workspace: currentWorkspace
        ? { id: currentWorkspace.id, slug: currentWorkspace.slug, name: currentWorkspace.name }
        : null,
    }),
    [locale, mode, isMobile, currentWorkspace],
  );

  // DnD is only fully active when groupBy === 'status'
  // (we disable cross-column drag for other groupings to keep status intact)
  const dndEnabled = groupBy === "status";

  // Column data is rebuilt from scratch on every recompute, including one a single
  // card's drag triggers; stabilizeColumns hands back the previous object of every
  // column whose content did not change, so the column memo holds across a drag.
  const columnCacheRef = useRef<Map<string, BoardColumnData>>(new Map());
  const columnData = useMemo(() => {
    const built = rawColumns.map<BoardColumnData>((col) => {
      const category = col.status?.category;
      const closed =
        groupBy === "status" && col.status && isClosedCategory(col.status.category)
          ? closedInfoByStatus[col.id]
          : undefined;
      const raw = tasksByColumn[col.id] ?? [];
      return {
        id: col.id,
        kind: !col.status ? "group" : category === "triage" ? "triage" : "status",
        title: col.title,
        color: col.color,
        status: col.status ? toStatusData(col.status) : undefined,
        tasks: raw.map((task) =>
          toTaskData(task, category, task.checked_out_by ? holderNameById.get(task.checked_out_by) : undefined, shortTags, ownedVcs),
        ),
        totalCount: closed?.slice.hasMore && !closed.filtersActive ? closed.slice.total : raw.length,
        countLabel: columnCountLabel(raw.length, closed),
        wipLimit: null,
        isClosed: Boolean(col.status && isClosedCategory(col.status.category)),
        hasMore: Boolean(closed?.slice.hasMore),
        loadingMore: false,
        closedSlice: closed
          ? { loaded: closed.slice.loaded, total: closed.slice.total, filtersActive: closed.filtersActive }
          : undefined,
        gateStatusNameByTask: category === "triage" ? gateStatusByTask : undefined,
        group: col.priority
          ? { priority: col.priority }
          : col.assigneeId
            ? {
                assignee: {
                  id: col.assigneeId,
                  name: col.assigneeName,
                  type: col.assigneeType ?? "unassigned",
                  ...(col.assigneeType === "agent" && shortTags.has(col.assigneeId) ? { shortTag: shortTags.get(col.assigneeId) } : {}),
                },
              }
            : undefined,
      };
    });
    const { columns, cache } = stabilizeColumns(columnCacheRef.current, built);
    columnCacheRef.current = cache;
    return columns;
  }, [rawColumns, tasksByColumn, gateStatusByTask, groupBy, closedInfoByStatus, holderNameById, shortTags, ownedVcs]);

  // ----- Drag handlers -----

  // dnd-kit only reports a drag after the pointer has moved 5px, but the list must
  // not change under a pressed pointer before that either. The press arms the same
  // hold; if dnd-kit never activates (a click, a short hold), the release lets the
  // held refresh go — a macrotask later, so a real drop's own flush runs first.
  const dragActiveRef = useRef(false);
  const handleCardPointerDownCapture = useCallback(() => {
    markDragStart();
    const release = () => {
      window.removeEventListener("pointerup", release, true);
      window.removeEventListener("pointercancel", release, true);
      setTimeout(() => {
        if (!dragActiveRef.current) flushPendingRefetch();
      }, 0);
    };
    window.addEventListener("pointerup", release, true);
    window.addEventListener("pointercancel", release, true);
  }, [markDragStart, flushPendingRefetch]);

  const handleDragActiveChange = useCallback(
    (active: boolean) => {
      dragActiveRef.current = active;
      if (active) markDragStart();
      else flushPendingRefetch();
    },
    [markDragStart, flushPendingRefetch],
  );

  // Latest values for the stable actions below: a drag must not rebuild them.
  const latest = useRef({ tasksByColumn, gateStatusByTask, dndEnabled, closedInfoByStatus, cardFields, fieldCatalog });
  latest.current = { tasksByColumn, gateStatusByTask, dndEnabled, closedInfoByStatus, cardFields, fieldCatalog };

  const moveTaskAction = useCallback(
    async (taskId: string, toColumnId: string, position: { index: number }) => {
      const L = latest.current;
      if (!L.dndEnabled) return;
      const source = Object.entries(L.tasksByColumn).find(([, rows]) => rows.some((t) => t.id === taskId));
      const task = source?.[1].find((t) => t.id === taskId);
      if (!source || !task) return;
      const plan = planMoveToIndex({
        task,
        sourceColId: source[0],
        targetColId: toColumnId,
        index: position.index,
        tasksByColumn: L.tasksByColumn,
        gateStatusByTask: L.gateStatusByTask,
      });
      if (!plan) return;
      await moveTask(task.id, plan);
    },
    [moveTask],
  );

  const handleMove = useCallback(
    async (move: MoveRequest) => {
      if (move.item.kind !== "task" || move.to.kind !== "status") return;
      await moveTaskAction(move.item.id, move.to.id, { index: move.index });
    },
    [moveTaskAction],
  );

  const registryVersion = useSlotRegistryVersion();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- a registration change must re-resolve the nested views
  const components = useMemo(() => resolveBoardComponents(), [registryVersion]);

  const renderDragOverlay = useCallback(
    (item: { id: string }) => {
      const task = useTaskStore.getState().tasks.find((t) => t.id === item.id);
      if (!task) return null;
      const { Card } = components;
      const fields = resolveCardFields(latest.current.fieldCatalog, latest.current.cardFields);
      return (
        <div className="w-72">
          <Card
            data={{ task: toTaskData(task, undefined, undefined, shortTags, ownedVcs), column: { id: "", kind: "status" }, fields, selected: false, isOverlay: true }}
            actions={{ openTask: () => {} }}
            themeSettings={themeSettings}
            ctx={ctx}
          />
        </div>
      );
    },
    [components, themeSettings, ctx],
  );

  // ----- New task helpers -----

  // Sync current filter state to saved-view store (so ViewTabBar can save it).
  // Capture and restore both go through board-saved-view.ts, which keeps the
  // two halves symmetric — see that module for why `custom_fields` carries
  // `cfFilters` rather than the dead `customFieldFilters` state.
  const {
    pendingView,
    clearPendingView,
    setCurrentViewState,
    resetFiltersRequest,
    clearResetFiltersRequest,
  } = useSavedViewStore();
  useEffect(() => {
    setCurrentViewState(
      buildBoardSavedViewState({
        searchQuery,
        priorityFilter,
        assigneeFilter,
        assigneeIdsFilter,
        cfFilters,
        selectedTags,
        groupBy,
        showClosed,
        showSubtasks,
        humanGateOnly,
        sortBy,
      }),
    );
  }, [
    searchQuery,
    priorityFilter,
    assigneeFilter,
    assigneeIdsFilter,
    cfFilters,
    selectedTags,
    groupBy,
    showClosed,
    showSubtasks,
    humanGateOnly,
    sortBy,
    setCurrentViewState,
  ]);

  // Listen for saved view applied from ViewTabBar
  useEffect(() => {
    if (pendingView && pendingView.view_type === "board") {
      const restored = readBoardSavedViewState(pendingView.filters ?? {}, pendingView.sort_by);
      setSearchQuery(restored.searchQuery);
      setPriorityFilter(restored.priorityFilter);
      setAssigneeFilter(restored.assigneeFilter);
      setAssigneeIdsFilter(restored.assigneeIdsFilter);
      setCFFilters(restored.cfFilters);
      setSelectedTags(restored.selectedTags);
      setGroupBy(restored.groupBy);
      setShowClosed(restored.showClosed);
      setShowSubtasks(restored.showSubtasks);
      setHumanGateOnly(restored.humanGateOnly);
      // No sort_by on the view: keep whatever sort is currently active.
      if (restored.sortBy) setSortBy(restored.sortBy);
      clearPendingView();
    }
  }, [pendingView, clearPendingView]);

  // Reset filters to defaults (Saved Views menu → "Reset filters"). Leaves
  // groupBy/sortBy untouched — those are the board's layout, not a filter.
  useEffect(() => {
    if (!resetFiltersRequest) return;
    setSearchQuery(BOARD_FILTER_DEFAULTS.searchQuery);
    setPriorityFilter(BOARD_FILTER_DEFAULTS.priorityFilter);
    setAssigneeFilter(BOARD_FILTER_DEFAULTS.assigneeFilter);
    setAssigneeIdsFilter(BOARD_FILTER_DEFAULTS.assigneeIdsFilter);
    setCFFilters(BOARD_FILTER_DEFAULTS.cfFilters);
    setSelectedTags(BOARD_FILTER_DEFAULTS.selectedTags);
    setShowClosed(BOARD_FILTER_DEFAULTS.showClosed);
    setShowSubtasks(BOARD_FILTER_DEFAULTS.showSubtasks);
    setHumanGateOnly(BOARD_FILTER_DEFAULTS.humanGateOnly);
    setWorkingOnly(false);
    clearResetFiltersRequest();
  }, [resetFiltersRequest, clearResetFiltersRequest]);

  const openCreateDialog = useCallback(
    (statusId?: string) => {
      if (!wsSlug || !currentProject) return;
      const query = statusId ? `?status=${statusId}` : "";
      navigate(`/w/${wsSlug}/p/${currentProject.slug}/new${query}`);
    },
    [wsSlug, currentProject, navigate],
  );

  const [recurringOpen, setRecurringOpen] = useState(false);

  const { openTask: coreOpenTask, quickEdit: coreQuickEdit } = useCoreActions();
  const openTaskById = useCallback(
    (taskId: string, opts?: { newTab?: boolean }) => {
      // Full-screen task page on every viewport. Was gated to isMobile in #70,
      // leaving desktop on the slide-over (Alex 2026-07-07 — fullscreen only).
      if (wsSlug && currentProject) {
        if (opts?.newTab) window.open(taskPath(wsSlug, currentProject.slug, taskId), "_blank", "noopener");
        else coreOpenTask(taskId);
      } else {
        setSlideOverTaskId(taskId);
      }
    },
    [wsSlug, currentProject, coreOpenTask],
  );
  // The primitives inside the cards get one object that never changes: reading router context would re-render
  // every card on each navigation. The live values sit in a ref.
  const coreLive = useRef({ openTaskById, coreQuickEdit, wsSlug, slug: currentProject?.slug });
  coreLive.current = { openTaskById, coreQuickEdit, wsSlug, slug: currentProject?.slug };
  const coreActions = useMemo(
    () => ({
      openTask: (id: string) => coreLive.current.openTaskById(id),
      quickEdit: (id: string, field: QuickEditField, value: unknown) => coreLive.current.coreQuickEdit(id, field, value),
      taskHref: (id: string) => taskPath(coreLive.current.wsSlug, coreLive.current.slug, id),
    }),
    [],
  );

  const layoutActions = useMemo<BoardLayoutActions>(
    () => ({
      moveTask: moveTaskAction,
      openTask: openTaskById,
      quickEdit: (taskId: string, patch: QuickEditPatch) => coreQuickEdit(taskId, patch.field, patch.value),
      createTask: canCreate ? openCreateDialog : undefined,
      loadMore: (columnId: string) => {
        const info = latest.current.closedInfoByStatus[columnId];
        return info ? info.onLoadMore(columnId) : Promise.resolve();
      },
      retry: () => {
        if (currentProject) void reloadBoard(currentProject.id);
      },
    }),
    [moveTaskAction, openTaskById, coreQuickEdit, openCreateDialog, canCreate, currentProject, reloadBoard],
  );

  const boardReady = listComplete && listProjectId === currentProject?.id;
  const columnsAsync: Async<BoardColumnData[]> = !boardReady
    ? listError && !isLoading && listProjectId === currentProject?.id
      ? { state: "error", error: { code: "load_failed", message: t("refresh.failedInitial") }, retry: layoutActions.retry }
      : { state: "loading" }
    : listProjectId === currentProject?.id && listError
      ? { state: "error", error: { code: "refresh_failed", message: t("refresh.failedPartial") }, value: columnData, retry: layoutActions.retry }
      : isLoading
        ? { state: "refreshing", value: columnData }
        : { state: "ready", value: columnData };

  const layoutData = {
    project: { id: currentProject?.id ?? "", slug: currentProject?.slug ?? "", name: currentProject?.name ?? "" },
    groupBy,
    sortBy: sortBy as BoardSortBy,
    columns: columnsAsync,
    dnd: {
      enabled: dndEnabled && canMove,
      reason: !dndEnabled ? ("group_by" as const) : !canMove ? ("no_permission" as const) : undefined,
      nonDroppableColumnIds: columnData.filter((c) => c.kind !== "status").map((c) => c.id),
    },
    availableFields: fieldCatalog,
    cardFields,
    emptyReason: statuses.length === 0 ? ("no_statuses" as const) : ("no_matches" as const),
    closedPartial:
      showClosed && groupBy !== "status" && boardReady ? closedPartial : null,
    components,
  };
  const Layout = withSlotFallback("board.layout", resolveSlotView("board.layout"));

  // ----- Render -----

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
    <div className="flex flex-col gap-3">
      {/* Toolbar */}
      <div className="mesh-mobile-board-toolbar flex flex-wrap items-center gap-2">
        <BoardToolbar
          groupBy={groupBy}
          onGroupByChange={setGroupBy}
          sortBy={sortBy}
          onSortByChange={setSortBy}
          showClosed={showClosed}
          onShowClosedChange={setShowClosed}
          humanGateOnly={humanGateOnly}
          onHumanGateOnlyChange={setHumanGateOnly}
          workingOnly={workingOnly}
          onWorkingOnlyChange={setWorkingOnly}
          showSubtasks={showSubtasks}
          onShowSubtasksChange={setShowSubtasks}
          searchQuery={searchQuery}
          onSearchQueryChange={setSearchQuery}
          priorityFilter={priorityFilter}
          onPriorityFilterChange={setPriorityFilter}
          assigneeFilter={assigneeFilter}
          onAssigneeFilterChange={setAssigneeFilter}
          assigneeCandidates={assigneeCandidates}
          assigneeIdsFilter={assigneeIdsFilter}
          onAssigneeIdsFilterChange={setAssigneeIdsFilter}
          allTags={allTags}
          selectedTags={selectedTags}
          onTagsChange={setSelectedTags}
          cfFilters={cfFilters}
          onCFFiltersChange={setCFFilters}
          filterableFields={filterableFields}
          customFieldFilters={customFieldFilters}
          onCustomFieldFiltersChange={setCustomFieldFilters}
          onNewTask={() => openCreateDialog()}
          onNewRecurring={() => setRecurringOpen(true)}
        />
      </div>

      <CoreActionsProvider actions={coreActions}>
        {columnsAsync.state === "loading" || (columnsAsync.state === "error" && !columnsAsync.value) ? (
          <Layout data={layoutData} actions={layoutActions} themeSettings={themeSettings} ctx={ctx} />
        ) : (
          <DndProvider
            enabled={dndEnabled}
            onMove={handleMove}
            onDragActiveChange={handleDragActiveChange}
            renderOverlay={renderDragOverlay}
          >
            <div style={{ display: "contents" }} onPointerDownCapture={handleCardPointerDownCapture}>
              <Layout data={layoutData} actions={layoutActions} themeSettings={themeSettings} ctx={ctx} />
            </div>
          </DndProvider>
        )}
      </CoreActionsProvider>

      <CreateRecurringDialog
        open={recurringOpen}
        onOpenChange={setRecurringOpen}
        projectId={currentProject?.id ?? ""}
      />

      {/* Task slide-over */}
      <TaskSlideOver
        taskId={slideOverTaskId}
        onClose={() => setSlideOverTaskId(null)}
        onTaskUpdated={() => {
          if (currentProject) fetchTasks(currentProject.id, { background: true });
        }}
      />
    </div>
  );
}
