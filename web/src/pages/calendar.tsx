import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import { addMonths, format, subMonths, startOfMonth, endOfMonth, startOfWeek, endOfWeek, eachDayOfInterval, isSameMonth, isToday } from "date-fns";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  useDraggable,
  useDroppable,
  type DragStartEvent,
  type DragEndEvent,
} from "@dnd-kit/core";
import { Calendar as CalendarIcon, CalendarOff, GripVertical, Plus } from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useWebSocket } from "@/hooks/use-websocket";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { CalendarToolbar } from "@/components/calendar-toolbar";
import { CreateTaskDialog } from "@/components/create-task-dialog";
import { TaskSlideOver } from "@/components/task-slide-over";
import { SavedViewsMenu } from "@/components/saved-views-menu";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import type { Task, WSMessage, SavedView } from "@/types";

// ---------------------------------------------------------------------------
// Drag data shape
// ---------------------------------------------------------------------------

interface DragData {
  task: Task;
  /** "date:YYYY-MM-DD" | "unscheduled" */
  source: string;
}

// ---------------------------------------------------------------------------
// Draggable task chip (used inside day cells)
// ---------------------------------------------------------------------------

interface DraggableTaskChipProps {
  task: Task;
  source: string;
  statusMap: Map<string, { name: string; color: string; category: string }>;
  onClick: (task: Task) => void;
  /** True while this chip is the active drag source (renders ghost in place) */
  isBeingDragged: boolean;
}

function DraggableTaskChip({
  task,
  source,
  statusMap,
  onClick,
  isBeingDragged,
}: DraggableTaskChipProps) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: task.id,
    data: { task, source } satisfies DragData,
  });

  const status = statusMap.get(task.status_id);
  const isDone =
    status?.category === "done" || status?.category === "cancelled";

  return (
    <div
      ref={setNodeRef}
      {...attributes}
      {...listeners}
      className={cn(
        "flex w-full cursor-grab items-center gap-1 rounded px-1.5 py-0.5 text-left text-[11px] leading-tight",
        "transition-colors hover:bg-muted active:cursor-grabbing",
        isDone && "line-through opacity-60",
        (isDragging || isBeingDragged) && "opacity-30",
      )}
      style={{
        borderLeft: `2px solid ${status?.color ?? "#9ca3af"}`,
        touchAction: "none",
      }}
      onClick={(e) => {
        // Suppress click when pointer has moved (drag scenario)
        if (!isDragging) {
          e.stopPropagation();
          onClick(task);
        }
      }}
    >
      <span className="min-w-0 flex-1 truncate">{task.title}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Draggable task row (used inside unscheduled sidebar)
// ---------------------------------------------------------------------------

interface DraggableUnscheduledRowProps {
  task: Task;
  statusMap: Map<string, { name: string; color: string; category: string }>;
  onClick: (task: Task) => void;
  isBeingDragged: boolean;
}

function DraggableUnscheduledRow({
  task,
  statusMap,
  onClick,
  isBeingDragged,
}: DraggableUnscheduledRowProps) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: task.id,
    data: { task, source: "unscheduled" } satisfies DragData,
  });

  const status = statusMap.get(task.status_id);

  return (
    <div
      ref={setNodeRef}
      {...attributes}
      {...listeners}
      style={{ touchAction: "none" }}
      className={cn(
        "flex w-full cursor-grab items-center gap-2 rounded-md border border-border px-2.5 py-2 text-left text-sm",
        "transition-colors hover:bg-muted/50 active:cursor-grabbing",
        (isDragging || isBeingDragged) && "opacity-30",
      )}
      onClick={(e) => {
        if (!isDragging) {
          e.stopPropagation();
          onClick(task);
        }
      }}
    >
      <GripVertical className="h-3 w-3 shrink-0 text-muted-foreground" />
      {status && (
        <span
          className="inline-block h-2 w-2 shrink-0 rounded-full"
          style={{ backgroundColor: status.color }}
        />
      )}
      <span className="min-w-0 flex-1 truncate">{task.title}</span>
      {task.priority !== "none" && (
        <Badge variant="outline" className="shrink-0 text-[10px]">
          {task.priority}
        </Badge>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Droppable day cell
// ---------------------------------------------------------------------------

interface DroppableDayCellProps {
  dateKey: string;
  dayTasks: Task[];
  inMonth: boolean;
  today: boolean;
  day: Date;
  activeTaskId: string | null;
  statusMap: Map<string, { name: string; color: string; category: string }>;
  onTaskClick: (task: Task) => void;
  onAddTask: (date: string) => void;
}

function DroppableDayCell({
  dateKey,
  dayTasks,
  inMonth,
  today,
  day,
  activeTaskId,
  statusMap,
  onTaskClick,
  onAddTask,
}: DroppableDayCellProps) {
  const { setNodeRef, isOver } = useDroppable({ id: `date:${dateKey}` });

  return (
    <div
      ref={setNodeRef}
      className={cn(
        "group relative flex flex-col border-b border-r border-border p-1 transition-colors",
        !inMonth && "bg-muted/20",
        isOver && "bg-primary/5 ring-2 ring-inset ring-primary/40",
      )}
    >
      {/* Day number + add button */}
      <div className="mb-0.5 flex items-center justify-between px-0.5">
        <span
          className={cn(
            "inline-flex h-6 w-6 items-center justify-center rounded-full text-xs",
            today
              ? "bg-primary font-semibold text-primary-foreground"
              : !inMonth
                ? "text-muted-foreground/50"
                : "text-foreground",
          )}
        >
          {format(day, "d")}
        </span>
        <button
          onClick={() => onAddTask(dateKey)}
          className="hidden h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground group-hover:flex"
          title="Add task"
        >
          <Plus className="h-3 w-3" />
        </button>
      </div>

      {/* Task chips */}
      <div className="flex-1 space-y-0.5 overflow-y-auto">
        {dayTasks.slice(0, 4).map((task) => (
          <DraggableTaskChip
            key={task.id}
            task={task}
            source={`date:${dateKey}`}
            statusMap={statusMap}
            onClick={onTaskClick}
            isBeingDragged={activeTaskId === task.id}
          />
        ))}
        {dayTasks.length > 4 && (
          <span className="block px-1.5 text-[10px] text-muted-foreground">
            +{dayTasks.length - 4} more
          </span>
        )}

        {/* Drop placeholder shown when something is dragged over an empty area */}
        {isOver && activeTaskId && dayTasks.every((t) => t.id !== activeTaskId) && (
          <div className="h-5 w-full rounded border-2 border-dashed border-primary/40 bg-primary/5" />
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// DnD-aware Calendar Grid (replaces <CalendarGrid>)
// ---------------------------------------------------------------------------

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

interface DndCalendarGridProps {
  currentMonth: Date;
  tasksByDate: Map<string, Task[]>;
  statusMap: Map<string, { name: string; color: string; category: string }>;
  activeTaskId: string | null;
  onTaskClick: (task: Task) => void;
  onAddTask: (date: string) => void;
}

function DndCalendarGrid({
  currentMonth,
  tasksByDate,
  statusMap,
  activeTaskId,
  onTaskClick,
  onAddTask,
}: DndCalendarGridProps) {
  const days = useMemo(() => {
    const monthStart = startOfMonth(currentMonth);
    const monthEnd = endOfMonth(currentMonth);
    const calStart = startOfWeek(monthStart, { weekStartsOn: 1 });
    const calEnd = endOfWeek(monthEnd, { weekStartsOn: 1 });
    return eachDayOfInterval({ start: calStart, end: calEnd });
  }, [currentMonth]);

  return (
    <div className="flex flex-1 flex-col overflow-hidden rounded-lg border border-border">
      {/* Weekday header */}
      <div className="grid grid-cols-7 border-b border-border bg-muted/30">
        {WEEKDAYS.map((day) => (
          <div
            key={day}
            className="px-2 py-1.5 text-center text-xs font-medium text-muted-foreground"
          >
            {day}
          </div>
        ))}
      </div>

      {/* Day cells */}
      <div className="grid flex-1 auto-rows-fr grid-cols-7">
        {days.map((day) => {
          const dateKey = format(day, "yyyy-MM-dd");
          const dayTasks = tasksByDate.get(dateKey) ?? [];
          const inMonth = isSameMonth(day, currentMonth);
          const todayDay = isToday(day);

          return (
            <DroppableDayCell
              key={dateKey}
              dateKey={dateKey}
              dayTasks={dayTasks}
              inMonth={inMonth}
              today={todayDay}
              day={day}
              activeTaskId={activeTaskId}
              statusMap={statusMap}
              onTaskClick={onTaskClick}
              onAddTask={onAddTask}
            />
          );
        })}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// DnD-aware Unscheduled sidebar (replaces <CalendarUnscheduled>)
// ---------------------------------------------------------------------------

interface DndCalendarUnscheduledProps {
  tasks: Task[];
  statusMap: Map<string, { name: string; color: string; category: string }>;
  activeTaskId: string | null;
  onTaskClick: (task: Task) => void;
}

function DndCalendarUnscheduled({
  tasks,
  statusMap,
  activeTaskId,
  onTaskClick,
}: DndCalendarUnscheduledProps) {
  const { setNodeRef, isOver } = useDroppable({ id: "unscheduled" });

  return (
    <div
      ref={setNodeRef}
      className={cn(
        "flex-1 overflow-y-auto p-2 transition-colors",
        isOver && "bg-primary/5 ring-2 ring-inset ring-primary/40 rounded-b-lg",
      )}
    >
      {tasks.length === 0 && !isOver ? (
        <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
          <CalendarOff className="mb-2 h-8 w-8" />
          <p className="text-sm">No unscheduled tasks</p>
        </div>
      ) : (
        <div className="space-y-1.5">
          {tasks.map((task) => (
            <DraggableUnscheduledRow
              key={task.id}
              task={task}
              statusMap={statusMap}
              onClick={onTaskClick}
              isBeingDragged={activeTaskId === task.id}
            />
          ))}

          {/* Drop placeholder when hovering with a scheduled task */}
          {isOver && activeTaskId && tasks.every((t) => t.id !== activeTaskId) && (
            <div className="h-8 w-full rounded border-2 border-dashed border-primary/40 bg-primary/5" />
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// DnD Overlay card (shown while dragging)
// ---------------------------------------------------------------------------

function DragOverlayCard({ task, statusMap }: { task: Task; statusMap: Map<string, { name: string; color: string; category: string }> }) {
  const status = statusMap.get(task.status_id);
  return (
    <div
      className="flex w-48 cursor-grabbing items-center gap-1.5 rounded px-2 py-1 text-[11px] font-medium shadow-lg ring-2 ring-primary/30"
      style={{
        borderLeft: `3px solid ${status?.color ?? "#9ca3af"}`,
        backgroundColor: "var(--background)",
        color: "var(--foreground)",
      }}
    >
      <span className="min-w-0 flex-1 truncate">{task.title}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Calendar page
// ---------------------------------------------------------------------------

export function CalendarPage() {
  const { wsSlug } = useParams();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();
  const { tasks, isLoading, fetchTasks } = useTaskStore();
  const { fetchFields: fetchCustomFields } = useCustomFieldStore();

  // Calendar state
  const [currentMonth, setCurrentMonth] = useState(() => new Date());
  const [showUnscheduled, setShowUnscheduled] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");

  // Dialog state
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogDueDate, setDialogDueDate] = useState<string | undefined>();

  // Slide-over state
  const [slideOverTaskId, setSlideOverTaskId] = useState<string | null>(null);

  // DnD state — track which task is being dragged
  const [activeTask, setActiveTask] = useState<Task | null>(null);

  // Status map for quick lookups
  const statusMap = useMemo(() => {
    const map = new Map<string, { name: string; color: string; category: string }>();
    for (const s of statuses) {
      map.set(s.id, { name: s.name, color: s.color, category: s.category });
    }
    return map;
  }, [statuses]);

  // WS debounced refetch
  const refetchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const debouncedRefetch = useCallback(() => {
    if (refetchTimer.current) clearTimeout(refetchTimer.current);
    refetchTimer.current = setTimeout(() => {
      if (currentProject) fetchTasks(currentProject.id);
    }, 500);
  }, [currentProject, fetchTasks]);

  useWebSocket({
    workspaceSlug: wsSlug,
    projectId: currentProject?.id,
    onEvent: useCallback(
      (event: WSMessage) => {
        if (event.type === "summary") {
          const data = event.data as Record<string, unknown>;
          toast((data.subject as string) || "Agent summary");
        }
        if (
          event.type === "status_change" ||
          event.type === "context_update" ||
          event.type === "dependency_resolved" ||
          event.type === "custom"
        ) {
          debouncedRefetch();
        }
      },
      [debouncedRefetch],
    ),
  });

  // Initial data fetch
  useEffect(() => {
    if (currentProject) {
      fetchStatuses(currentProject.id);
      fetchTasks(currentProject.id);
      fetchCustomFields(currentProject.id).catch(() => {});
    }
  }, [currentProject, fetchStatuses, fetchTasks, fetchCustomFields]);

  // Filter tasks by search query
  const filteredTasks = useMemo(() => {
    if (!searchQuery) return tasks;
    const q = searchQuery.toLowerCase();
    return tasks.filter(
      (t) =>
        t.title.toLowerCase().includes(q) ||
        (t.labels ?? []).some((l) => l.toLowerCase().includes(q)),
    );
  }, [tasks, searchQuery]);

  // Partition tasks into scheduled (with due_date) and unscheduled
  const { scheduled, unscheduled } = useMemo(() => {
    const sched: Task[] = [];
    const unsched: Task[] = [];
    for (const task of filteredTasks) {
      // Skip subtasks from calendar view
      if (task.parent_task_id) continue;
      if (task.due_date) {
        sched.push(task);
      } else {
        unsched.push(task);
      }
    }
    return { scheduled: sched, unscheduled: unsched };
  }, [filteredTasks]);

  // Group scheduled tasks by date (YYYY-MM-DD)
  const tasksByDate = useMemo(() => {
    const map = new Map<string, Task[]>();
    for (const task of scheduled) {
      const dateKey = format(new Date(task.due_date!), "yyyy-MM-dd");
      const arr = map.get(dateKey) ?? [];
      arr.push(task);
      map.set(dateKey, arr);
    }
    return map;
  }, [scheduled]);

  // Navigation handlers
  const handlePrevMonth = () => setCurrentMonth((d) => subMonths(d, 1));
  const handleNextMonth = () => setCurrentMonth((d) => addMonths(d, 1));
  const handleToday = () => setCurrentMonth(new Date());

  // Add task on specific date
  const handleAddTask = (date: string) => {
    setDialogDueDate(date);
    setDialogOpen(true);
  };

  // Task click -> slide-over
  const handleTaskClick = useCallback((task: Task) => {
    setSlideOverTaskId(task.id);
  }, []);

  // Saved view application
  const handleApplyView = useCallback(
    (view: SavedView) => {
      if (view.filters) {
        const f = view.filters as Record<string, unknown>;
        if (typeof f.search === "string") setSearchQuery(f.search);
      }
    },
    [],
  );

  // ---------------------------------------------------------------------------
  // DnD sensors — require 5px movement before drag starts (preserves clicks)
  // ---------------------------------------------------------------------------

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    }),
  );

  const handleDragStart = useCallback((event: DragStartEvent) => {
    const data = event.active.data.current as DragData | undefined;
    if (data?.task) setActiveTask(data.task);
  }, []);

  const handleDragEnd = useCallback(
    async (event: DragEndEvent) => {
      setActiveTask(null);

      const { active, over } = event;
      if (!over) return;

      const data = active.data.current as DragData | undefined;
      if (!data) return;

      const draggedTask = data.task;
      const targetId = String(over.id); // "date:YYYY-MM-DD" | "unscheduled"

      if (targetId === data.source) return; // dropped back on origin — no-op

      if (targetId === "unscheduled") {
        // Clear due_date → move to unscheduled
        if (!draggedTask.due_date) return; // already unscheduled

        // Optimistic update
        const taskStore = useTaskStore.getState();
        taskStore.updateTask(draggedTask.id, { due_date: null }).catch(() => {
          toast("Failed to remove due date");
          if (currentProject) fetchTasks(currentProject.id);
        });
        return;
      }

      if (targetId.startsWith("date:")) {
        const newDate = targetId.slice(5); // "YYYY-MM-DD"
        const currentDate = draggedTask.due_date
          ? format(new Date(draggedTask.due_date), "yyyy-MM-dd")
          : null;

        if (currentDate === newDate) return; // same date — no-op

        const taskStore = useTaskStore.getState();
        taskStore.updateTask(draggedTask.id, { due_date: `${newDate}T00:00:00Z` }).catch(() => {
          toast("Failed to update due date");
          if (currentProject) fetchTasks(currentProject.id);
        });
      }
    },
    [currentProject, fetchTasks],
  );

  const handleDragCancel = useCallback(() => {
    setActiveTask(null);
  }, []);

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  if (!currentProject) {
    return (
      <div className="flex h-full items-center justify-center">
        <Skeleton className="h-8 w-48" />
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col gap-3 p-4">
      {/* Toolbar */}
      <div className="flex items-center gap-3">
        <SavedViewsMenu
          projectId={currentProject.id}
          currentViewType="calendar"
          onApplyView={handleApplyView}
        />
        <CalendarToolbar
          currentMonth={currentMonth}
          onPrevMonth={handlePrevMonth}
          onNextMonth={handleNextMonth}
          onToday={handleToday}
          unscheduledCount={unscheduled.length}
          showUnscheduled={showUnscheduled}
          onToggleUnscheduled={() => setShowUnscheduled((v) => !v)}
          searchQuery={searchQuery}
          onSearchChange={setSearchQuery}
        />
      </div>

      {/* Main content */}
      {isLoading ? (
        <div className="flex flex-1 items-center justify-center">
          <div className="text-center">
            <CalendarIcon className="mx-auto mb-3 h-10 w-10 animate-pulse text-muted-foreground" />
            <p className="text-sm text-muted-foreground">Loading calendar...</p>
          </div>
        </div>
      ) : (
        <DndContext
          sensors={sensors}
          onDragStart={handleDragStart}
          onDragEnd={handleDragEnd}
          onDragCancel={handleDragCancel}
        >
          <div className="flex flex-1 gap-3 overflow-hidden">
            {/* Calendar grid */}
            <DndCalendarGrid
              currentMonth={currentMonth}
              tasksByDate={tasksByDate}
              statusMap={statusMap}
              activeTaskId={activeTask?.id ?? null}
              onTaskClick={handleTaskClick}
              onAddTask={handleAddTask}
            />

            {/* Unscheduled sidebar */}
            {showUnscheduled && (
              <div className="flex w-64 shrink-0 flex-col rounded-lg border border-border">
                <div className="border-b border-border px-3 py-2">
                  <h3 className="text-sm font-semibold">Unscheduled</h3>
                  <p className="text-xs text-muted-foreground">
                    {unscheduled.length} task{unscheduled.length !== 1 ? "s" : ""}
                  </p>
                </div>
                <DndCalendarUnscheduled
                  tasks={unscheduled}
                  statusMap={statusMap}
                  activeTaskId={activeTask?.id ?? null}
                  onTaskClick={handleTaskClick}
                />
              </div>
            )}
          </div>

          {/* Drag overlay — floating card shown under pointer */}
          <DragOverlay dropAnimation={null}>
            {activeTask ? (
              <DragOverlayCard task={activeTask} statusMap={statusMap} />
            ) : null}
          </DragOverlay>
        </DndContext>
      )}

      {/* Create task dialog */}
      <CreateTaskDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        defaultDueDate={dialogDueDate}
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
