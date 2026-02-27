import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import { addMonths, format, subMonths } from "date-fns";
import { Calendar as CalendarIcon } from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useWebSocket } from "@/hooks/use-websocket";
import { Skeleton } from "@/components/ui/skeleton";
import { CalendarToolbar } from "@/components/calendar-toolbar";
import { CalendarGrid } from "@/components/calendar-grid";
import { CalendarUnscheduled } from "@/components/calendar-unscheduled";
import { CreateTaskDialog } from "@/components/create-task-dialog";
import { TaskSlideOver } from "@/components/task-slide-over";
import { SavedViewsMenu } from "@/components/saved-views-menu";
import { toast } from "@/components/ui/toast";
import type { Task, WSMessage, SavedView } from "@/types";

export function CalendarPage() {
  const { wsSlug, projectSlug } = useParams();
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
      // due_date comes as ISO string or YYYY-MM-DD
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
  const handleTaskClick = (task: Task) => {
    setSlideOverTaskId(task.id);
  };

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
        <div className="flex flex-1 gap-3 overflow-hidden">
          {/* Calendar grid */}
          <CalendarGrid
            currentMonth={currentMonth}
            tasksByDate={tasksByDate}
            statusMap={statusMap}
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
              <div className="flex-1 overflow-y-auto p-2">
                <CalendarUnscheduled
                  tasks={unscheduled}
                  statusMap={statusMap}
                  onTaskClick={handleTaskClick}
                />
              </div>
            </div>
          )}
        </div>
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
