import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import {
  DndContext,
  DragOverlay,
  closestCorners,
  PointerSensor,
  useSensor,
  useSensors,
  useDroppable,
  type DragStartEvent,
  type DragEndEvent,
  type DragOverEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  verticalListSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  AlertTriangle,
  Columns3,
  Flag,
  Plus,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useWebSocket } from "@/hooks/use-websocket";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { TaskCard } from "@/components/task-card";
import { CreateTaskDialog } from "@/components/create-task-dialog";
import { TaskSlideOver } from "@/components/task-slide-over";
import { toast } from "@/components/ui/toast";
import { BoardToolbar, type GroupBy, type SortBy } from "@/components/board-toolbar";
import { useSavedViewStore } from "@/stores/saved-view-store";
import { CreateRecurringDialog } from "@/components/create-recurring-dialog";
import { AssigneeAvatar } from "@/components/assignee-avatar";
import { applyViewFilters, type CFFilters } from "@/components/view-filters";
import type { Task, TaskStatus, WSMessage, Priority, StatusCategory } from "@/types";

// ---------------------------------------------------------------------------
// Priority metadata (for GroupBy=priority columns)
// ---------------------------------------------------------------------------

const PRIORITY_ORDER: Record<Priority, number> = {
  urgent: 0,
  high: 1,
  medium: 2,
  low: 3,
  none: 4,
};

const PRIORITY_COLORS: Record<Priority, string> = {
  urgent: "#ef4444",
  high: "#f97316",
  medium: "#eab308",
  low: "#60a5fa",
  none: "#9ca3af",
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
// Sortable task card wrapper
// ---------------------------------------------------------------------------

interface SortableTaskCardProps {
  task: Task;
  columnId: string;
  statusCategory?: StatusCategory;
  onClick: () => void;
}

function SortableTaskCard({ task, columnId, statusCategory, onClick }: SortableTaskCardProps) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: task.id,
    data: { task, columnId },
  });

  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition: transition ?? undefined,
    opacity: isDragging ? 0.4 : 1,
  };

  return (
    <div ref={setNodeRef} style={style} {...attributes} {...listeners}>
      <TaskCard task={task} isDragging={isDragging} statusCategory={statusCategory} onClick={onClick} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Droppable column
// ---------------------------------------------------------------------------

interface BoardColumnProps {
  col: BoardCol;
  tasks: Task[];
  dndEnabled: boolean;
  onAddTask: (statusId?: string) => void;
  onTaskClick: (task: Task) => void;
}

function BoardColumn({ col, tasks, dndEnabled, onAddTask, onTaskClick }: BoardColumnProps) {
  const { setNodeRef, isOver } = useDroppable({ id: `column-${col.id}` });

  const taskIds = useMemo(() => tasks.map((t) => t.id), [tasks]);

  return (
    <div className="w-72 shrink-0">
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          {/* Column header indicator */}
          {col.priority ? (
            <PriorityIcon priority={col.priority} />
          ) : col.assigneeId ? (
            <AssigneeAvatar
              name={col.assigneeName}
              type={col.assigneeType}
              size="sm"
            />
          ) : (
            <div
              className="h-2.5 w-2.5 rounded-full"
              style={{ backgroundColor: col.color }}
            />
          )}
          <span className="text-sm font-semibold">{col.title}</span>
          <Badge variant="secondary" className="text-xs">
            {tasks.length}
          </Badge>
        </div>
        {col.status && (
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            onClick={() => onAddTask(col.status!.id)}
          >
            <Plus className="h-3 w-3" />
          </Button>
        )}
      </div>

      <div
        ref={setNodeRef}
        className={
          "min-h-[60px] space-y-2 rounded-xl bg-muted/50 p-2 transition-colors" +
          (isOver ? " ring-2 ring-primary/30 bg-muted/70" : "") +
          (!dndEnabled ? " opacity-90" : "")
        }
      >
        <SortableContext
          items={dndEnabled ? taskIds : []}
          strategy={verticalListSortingStrategy}
        >
          {tasks.map((task) => (
            <SortableTaskCard
              key={task.id}
              task={task}
              columnId={col.id}
              statusCategory={col.status?.category}
              onClick={() => onTaskClick(task)}
            />
          ))}
        </SortableContext>

        {tasks.length === 0 && (
          <p className="py-4 text-center text-xs text-muted-foreground">
            No tasks
          </p>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tiny priority icon for column headers
// ---------------------------------------------------------------------------

function PriorityIcon({ priority }: { priority: Priority }) {
  const color = PRIORITY_COLORS[priority];
  const Icon = priority === "urgent" ? AlertTriangle : Flag;
  return <Icon className="h-3.5 w-3.5" style={{ color }} />;
}

// ---------------------------------------------------------------------------
// Sort helper — sort tasks within a column by the given SortBy value
// ---------------------------------------------------------------------------

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
// Position calculation helpers
// ---------------------------------------------------------------------------

function calculatePosition(tasks: Task[], targetIndex: number): number {
  if (tasks.length === 0) return 1;
  if (targetIndex <= 0) {
    const first = tasks[0];
    return first ? first.position - 1 : 1;
  }
  if (targetIndex >= tasks.length) {
    const last = tasks[tasks.length - 1];
    return last ? last.position + 1 : 1;
  }
  const prev = tasks[targetIndex - 1];
  const next = tasks[targetIndex];
  if (prev && next) return (prev.position + next.position) / 2;
  return targetIndex;
}

// ---------------------------------------------------------------------------
// Board page
// ---------------------------------------------------------------------------

export function BoardPage() {
  const { wsSlug, projectSlug } = useParams();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();
  const { tasks, tasksByStatus, isLoading, fetchTasks, moveTask } = useTaskStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();

  // Dialog state
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogStatusId, setDialogStatusId] = useState<string | undefined>();

  // Slide-over state
  const [slideOverTaskId, setSlideOverTaskId] = useState<string | null>(null);

  // Drag state
  const [activeTask, setActiveTask] = useState<Task | null>(null);

  // ---- Toolbar state ----
  const [groupBy, setGroupBy] = useState<GroupBy>("status");
  const [sortBy, setSortBy] = useState<SortBy>("manual");
  const [showClosed, setShowClosed] = useState(false);
  const [showSubtasks, setShowSubtasks] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [priorityFilter, setPriorityFilter] = useState<string>("all");
  const [assigneeFilter, setAssigneeFilter] = useState<string>("all");
  const [customFieldFilters, setCustomFieldFilters] = useState<Record<string, unknown>>({});
  // New filter state
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [cfFilters, setCFFilters] = useState<CFFilters>({});

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    }),
  );

  // Debounce ref to avoid re-fetching too rapidly on burst events.
  const refetchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const debouncedRefetch = useCallback(() => {
    if (refetchTimer.current) clearTimeout(refetchTimer.current);
    refetchTimer.current = setTimeout(() => {
      if (currentProject) fetchTasks(currentProject.id);
    }, 500);
  }, [currentProject, fetchTasks]);

  // Listen for real-time WebSocket events.
  useWebSocket({
    workspaceSlug: wsSlug,
    projectId: currentProject?.id,
    onEvent: useCallback(
      (event: WSMessage) => {
        const eventType = event.type;
        if (eventType === "summary") {
          const data = event.data as Record<string, unknown>;
          const subject = (data.subject as string) || "Agent summary";
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
      [debouncedRefetch],
    ),
  });

  useEffect(() => {
    if (currentProject) {
      fetchStatuses(currentProject.id);
      fetchTasks(currentProject.id);
      fetchCustomFields(currentProject.id).catch(() => {
        // Custom fields API may not be available yet
      });
    }
  }, [currentProject, fetchStatuses, fetchTasks, fetchCustomFields]);

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

  // ---------------------------------------------------------------------------
  // Base task filter (search + priority + assignee + tags + CF + subtasks)
  // Applied before grouping.
  // ---------------------------------------------------------------------------

  const filteredTasks = useMemo(() => {
    // First pass: basic filters (subtasks, search, priority, assignee)
    const basic = tasks.filter((task) => {
      if (!showSubtasks && task.parent_task_id) return false;
      if (searchQuery && !task.title.toLowerCase().includes(searchQuery.toLowerCase())) {
        return false;
      }
      if (priorityFilter !== "all" && task.priority !== priorityFilter) return false;
      if (assigneeFilter !== "all" && task.assignee_type !== assigneeFilter) return false;
      return true;
    });

    // Second pass: tag + CF filters via shared pure function
    return applyViewFilters(basic, selectedTags, cfFilters);
  }, [tasks, showSubtasks, searchQuery, priorityFilter, assigneeFilter, selectedTags, cfFilters]);

  // ---------------------------------------------------------------------------
  // Build columns + task groups based on groupBy
  // ---------------------------------------------------------------------------

  const { columns, tasksByColumn } = useMemo((): {
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
      for (const col of cols) {
        const raw = tasksByStatus[col.id] ?? [];
        // Apply global filters
        const filtered = raw.filter((t) => filteredTasks.some((ft) => ft.id === t.id));
        byCol[col.id] = sortTasks(filtered, sortBy);
      }

      return { columns: cols, tasksByColumn: byCol };
    }

    if (groupBy === "priority") {
      // ------ Priority grouping ------
      const priorities: Priority[] = ["urgent", "high", "medium", "low", "none"];

      const cols: BoardCol[] = priorities.map((p) => ({
        id: `priority-${p}`,
        title:
          p === "urgent"
            ? "Urgent"
            : p === "high"
              ? "High"
              : p === "medium"
                ? "Medium"
                : p === "low"
                  ? "Low"
                  : "No Priority",
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
      assignees.push({ id: "unassigned", name: "Unassigned", type: "unassigned" });
    }

    const cols: BoardCol[] = assignees.map((a) => ({
      id: `assignee-${a.id}`,
      title: a.name ?? "Unassigned",
      color: "#9ca3af",
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
  ]);

  // DnD is only fully active when groupBy === 'status'
  // (we disable cross-column drag for other groupings to keep status intact)
  const dndEnabled = groupBy === "status";

  // ----- Drag handlers -----

  const handleDragStart = useCallback((event: DragStartEvent) => {
    const data = event.active.data.current as { task: Task; columnId: string } | undefined;
    if (data?.task) setActiveTask(data.task);
  }, []);

  const findColumnId = useCallback(
    (overId: string | number): string | null => {
      const colPrefix = "column-";
      const strId = String(overId);
      if (strId.startsWith(colPrefix)) return strId.slice(colPrefix.length);

      // Otherwise it is a task id; find which column it belongs to
      for (const col of columns) {
        const colTasks = tasksByColumn[col.id] ?? [];
        if (colTasks.some((t) => t.id === strId)) return col.id;
      }
      return null;
    },
    [columns, tasksByColumn],
  );

  const handleDragOver = useCallback((_event: DragOverEvent) => {
    // Handled in DragEnd
  }, []);

  const handleDragEnd = useCallback(
    async (event: DragEndEvent) => {
      setActiveTask(null);
      if (!dndEnabled) return;

      const { active, over } = event;
      if (!over) return;

      const activeData = active.data.current as { task: Task; columnId: string } | undefined;
      if (!activeData) return;

      const draggedTask = activeData.task;
      const sourceColId = activeData.columnId;
      const targetColId = findColumnId(over.id);

      if (!targetColId) return;

      // For status grouping, targetColId IS the status id
      const targetStatusId = targetColId;

      const targetTasks = (tasksByColumn[targetColId] ?? []).filter(
        (t) => t.id !== draggedTask.id,
      );

      let dropIndex = targetTasks.length;
      const overStr = String(over.id);
      if (!overStr.startsWith("column-")) {
        const idx = targetTasks.findIndex((t) => t.id === overStr);
        if (idx !== -1) dropIndex = idx;
      }

      const newPosition = calculatePosition(targetTasks, dropIndex);

      if (sourceColId === targetColId && draggedTask.position === newPosition) return;

      await moveTask(draggedTask.id, {
        status_id: targetStatusId,
        position: newPosition,
      });
    },
    [dndEnabled, findColumnId, tasksByColumn, moveTask],
  );

  const handleDragCancel = useCallback(() => setActiveTask(null), []);

  // ----- New task helpers -----

  // Listen for saved view applied from ViewTabBar
  const { pendingView, clearPendingView } = useSavedViewStore();
  useEffect(() => {
    if (pendingView && pendingView.view_type === "board") {
      const filters = pendingView.filters ?? {};
      setSearchQuery((filters.search as string) ?? "");
      setPriorityFilter((filters.priority as string) ?? "all");
      setAssigneeFilter((filters.assignee as string) ?? "all");
      setCustomFieldFilters((filters.custom_fields as Record<string, unknown>) ?? {});
      clearPendingView();
    }
  }, [pendingView, clearPendingView]);

  const openCreateDialog = useCallback((statusId?: string) => {
    setDialogStatusId(statusId);
    setDialogOpen(true);
  }, []);

  const [recurringOpen, setRecurringOpen] = useState(false);

  const handleTaskClick = useCallback((task: Task) => {
    setSlideOverTaskId(task.id);
  }, []);

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
      <div className="flex flex-wrap items-center gap-2">
        <BoardToolbar
          groupBy={groupBy}
          onGroupByChange={setGroupBy}
          sortBy={sortBy}
          onSortByChange={setSortBy}
          showClosed={showClosed}
          onShowClosedChange={setShowClosed}
          showSubtasks={showSubtasks}
          onShowSubtasksChange={setShowSubtasks}
          searchQuery={searchQuery}
          onSearchQueryChange={setSearchQuery}
          priorityFilter={priorityFilter}
          onPriorityFilterChange={setPriorityFilter}
          assigneeFilter={assigneeFilter}
          onAssigneeFilterChange={setAssigneeFilter}
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

      {/* Board */}
      {isLoading ? (
        <div className="flex gap-4 overflow-x-auto pb-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="w-72 shrink-0 space-y-3">
              <Skeleton className="h-8 w-full rounded-lg" />
              <Skeleton className="h-24 w-full rounded-lg" />
              <Skeleton className="h-24 w-full rounded-lg" />
            </div>
          ))}
        </div>
      ) : (
        <DndContext
          sensors={sensors}
          collisionDetection={closestCorners}
          onDragStart={handleDragStart}
          onDragOver={handleDragOver}
          onDragEnd={handleDragEnd}
          onDragCancel={handleDragCancel}
        >
          <div className="flex gap-4 overflow-x-auto pb-4">
            {columns.map((col) => (
              <BoardColumn
                key={col.id}
                col={col}
                tasks={tasksByColumn[col.id] ?? []}
                dndEnabled={dndEnabled}
                onAddTask={openCreateDialog}
                onTaskClick={handleTaskClick}
              />
            ))}

            {columns.length === 0 && (
              <div className="flex w-full items-center justify-center py-12">
                <div className="text-center">
                  <Columns3 className="mx-auto mb-4 h-12 w-12 text-muted-foreground" />
                  <h3 className="mb-2 text-lg font-semibold">
                    {statuses.length === 0
                      ? "No statuses configured"
                      : "No tasks match filters"}
                  </h3>
                  <p className="text-sm text-muted-foreground">
                    {statuses.length === 0
                      ? "Configure task statuses in project settings to use the board."
                      : "Try adjusting your filters or grouping."}
                  </p>
                </div>
              </div>
            )}
          </div>

          <DragOverlay>
            {activeTask ? (
              <div className="w-72">
                <TaskCard task={activeTask} isDragging />
              </div>
            ) : null}
          </DragOverlay>
        </DndContext>
      )}

      {/* Create task dialog */}
      <CreateTaskDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        defaultStatusId={dialogStatusId}
      />

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
          if (currentProject) fetchTasks(currentProject.id);
        }}
      />
    </div>
  );
}
