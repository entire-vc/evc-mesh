import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
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
  Columns3,
  GitBranch,
  List,
  Plus,
  Search,
  SlidersHorizontal,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useWebSocket } from "@/hooks/use-websocket";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { TaskCard } from "@/components/task-card";
import { CreateTaskDialog } from "@/components/create-task-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { toast } from "@/components/ui/toast";
import { SavedViewsMenu } from "@/components/saved-views-menu";
import type { Task, TaskStatus, CustomFieldDefinition, WSMessage, SavedView } from "@/types";

// ---------------------------------------------------------------------------
// Sortable task card wrapper
// ---------------------------------------------------------------------------

interface SortableTaskCardProps {
  task: Task;
  statusId: string;
  customFields?: CustomFieldDefinition[];
  onClick: () => void;
}

function SortableTaskCard({ task, statusId, customFields, onClick }: SortableTaskCardProps) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: task.id,
    data: { task, statusId },
  });

  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition: transition ?? undefined,
    opacity: isDragging ? 0.4 : 1,
  };

  return (
    <div ref={setNodeRef} style={style} {...attributes} {...listeners}>
      <TaskCard task={task} isDragging={isDragging} customFields={customFields} onClick={onClick} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Droppable column
// ---------------------------------------------------------------------------

interface BoardColumnProps {
  status: TaskStatus;
  tasks: Task[];
  customFields?: CustomFieldDefinition[];
  onAddTask: (statusId: string) => void;
  onTaskClick: (task: Task) => void;
}

function BoardColumn({ status, tasks, customFields, onAddTask, onTaskClick }: BoardColumnProps) {
  const { setNodeRef, isOver } = useDroppable({ id: `column-${status.id}` });

  const taskIds = useMemo(() => tasks.map((t) => t.id), [tasks]);

  return (
    <div className="w-72 shrink-0">
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <div
            className="h-2.5 w-2.5 rounded-full"
            style={{ backgroundColor: status.color }}
          />
          <span className="text-sm font-semibold">{status.name}</span>
          <Badge variant="secondary" className="text-xs">
            {tasks.length}
          </Badge>
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6"
          onClick={() => onAddTask(status.id)}
        >
          <Plus className="h-3 w-3" />
        </Button>
      </div>

      <div
        ref={setNodeRef}
        className={
          "min-h-[60px] space-y-2 rounded-xl bg-muted/50 p-2 transition-colors" +
          (isOver ? " ring-2 ring-primary/30 bg-muted/70" : "")
        }
      >
        <SortableContext items={taskIds} strategy={verticalListSortingStrategy}>
          {tasks.map((task) => (
            <SortableTaskCard
              key={task.id}
              task={task}
              statusId={status.id}
              customFields={customFields}
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
// Position calculation helpers
// ---------------------------------------------------------------------------

function calculatePosition(
  tasks: Task[],
  targetIndex: number,
): number {
  if (tasks.length === 0) {
    return 1;
  }

  // Inserting at the beginning
  if (targetIndex <= 0) {
    const first = tasks[0];
    return first ? first.position - 1 : 1;
  }

  // Inserting at the end
  if (targetIndex >= tasks.length) {
    const last = tasks[tasks.length - 1];
    return last ? last.position + 1 : 1;
  }

  // Inserting between two items
  const prev = tasks[targetIndex - 1];
  const next = tasks[targetIndex];
  if (prev && next) {
    return (prev.position + next.position) / 2;
  }

  return targetIndex;
}

// ---------------------------------------------------------------------------
// Board page
// ---------------------------------------------------------------------------

export function BoardPage() {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();
  const { tasksByStatus, isLoading, fetchTasks, moveTask } = useTaskStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();

  // Dialog state
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogStatusId, setDialogStatusId] = useState<string | undefined>();

  // Drag state
  const [activeTask, setActiveTask] = useState<Task | null>(null);

  // Filter state
  const [searchQuery, setSearchQuery] = useState("");
  const [priorityFilter, setPriorityFilter] = useState<string>("all");
  const [assigneeFilter, setAssigneeFilter] = useState<string>("all");

  // Custom field filters: { [fieldSlug]: filterValue }
  const [customFieldFilters, setCustomFieldFilters] = useState<
    Record<string, unknown>
  >({});

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    }),
  );

  // Debounce ref to avoid re-fetching too rapidly on burst events.
  const refetchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const debouncedRefetch = useCallback(() => {
    if (refetchTimer.current) {
      clearTimeout(refetchTimer.current);
    }
    refetchTimer.current = setTimeout(() => {
      if (currentProject) {
        fetchTasks(currentProject.id);
      }
    }, 500);
  }, [currentProject, fetchTasks]);

  // Listen for real-time WebSocket events.
  useWebSocket({
    workspaceSlug: wsSlug,
    projectId: currentProject?.id,
    onEvent: useCallback(
      (event: WSMessage) => {
        const eventType = event.type;

        // Show toast for summary events.
        if (eventType === "summary") {
          const data = event.data as Record<string, unknown>;
          const subject = (data.subject as string) || "Agent summary";
          toast(subject);
        }

        // Refresh board data on task-related events.
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

  // Filterable custom fields (select, checkbox, number)
  const filterableFields = useMemo(
    () =>
      customFieldDefs.filter((f) =>
        ["select", "checkbox", "number"].includes(f.field_type),
      ),
    [customFieldDefs],
  );

  // Filtered tasks by status
  const filteredTasksByStatus = useMemo(() => {
    const result: Record<string, Task[]> = {};
    for (const status of sortedStatuses) {
      const tasks = tasksByStatus[status.id] ?? [];
      result[status.id] = tasks.filter((task) => {
        if (
          searchQuery &&
          !task.title.toLowerCase().includes(searchQuery.toLowerCase())
        ) {
          return false;
        }
        if (
          priorityFilter !== "all" &&
          task.priority !== priorityFilter
        ) {
          return false;
        }
        if (
          assigneeFilter !== "all" &&
          task.assignee_type !== assigneeFilter
        ) {
          return false;
        }
        // Custom field filters
        for (const [slug, filterValue] of Object.entries(customFieldFilters)) {
          if (filterValue == null || filterValue === "" || filterValue === "all") {
            continue;
          }
          const fieldDef = customFieldDefs.find((f) => f.slug === slug);
          if (!fieldDef) continue;

          const taskValue = task.custom_fields?.[slug];

          if (fieldDef.field_type === "select") {
            if (taskValue !== filterValue) return false;
          } else if (fieldDef.field_type === "checkbox") {
            const boolFilter = filterValue === "checked";
            if (Boolean(taskValue) !== boolFilter) return false;
          } else if (fieldDef.field_type === "number") {
            const numVal = taskValue != null ? Number(taskValue) : null;
            const fv = filterValue as { min?: number; max?: number };
            if (fv.min != null && (numVal == null || numVal < fv.min)) return false;
            if (fv.max != null && (numVal == null || numVal > fv.max)) return false;
          }
        }
        return true;
      });
    }
    return result;
  }, [sortedStatuses, tasksByStatus, searchQuery, priorityFilter, assigneeFilter, customFieldFilters, customFieldDefs]);

  // ----- Drag handlers -----

  const handleDragStart = useCallback((event: DragStartEvent) => {
    const data = event.active.data.current as
      | { task: Task; statusId: string }
      | undefined;
    if (data?.task) {
      setActiveTask(data.task);
    }
  }, []);

  const findColumnId = useCallback(
    (overId: string | number): string | null => {
      // If the overId is "column-<statusId>" it is the droppable column itself
      const colPrefix = "column-";
      const strId = String(overId);
      if (strId.startsWith(colPrefix)) {
        return strId.slice(colPrefix.length);
      }

      // Otherwise it is a task id; find which column it belongs to
      for (const status of sortedStatuses) {
        const tasks = filteredTasksByStatus[status.id] ?? [];
        if (tasks.some((t) => t.id === strId)) {
          return status.id;
        }
      }
      return null;
    },
    [sortedStatuses, filteredTasksByStatus],
  );

  const handleDragOver = useCallback((_event: DragOverEvent) => {
    // We handle everything in DragEnd for simplicity, but DragOver
    // could be used for live preview reordering.
  }, []);

  const handleDragEnd = useCallback(
    async (event: DragEndEvent) => {
      setActiveTask(null);

      const { active, over } = event;
      if (!over) return;

      const activeData = active.data.current as
        | { task: Task; statusId: string }
        | undefined;
      if (!activeData) return;

      const draggedTask = activeData.task;
      const sourceStatusId = activeData.statusId;
      const targetStatusId = findColumnId(over.id);

      if (!targetStatusId) return;

      const targetTasks = (filteredTasksByStatus[targetStatusId] ?? []).filter(
        (t) => t.id !== draggedTask.id,
      );

      // Determine drop index
      let dropIndex = targetTasks.length; // default: end

      const overStr = String(over.id);
      if (!overStr.startsWith("column-")) {
        // Dropped on a specific task -- find its index
        const idx = targetTasks.findIndex((t) => t.id === overStr);
        if (idx !== -1) {
          dropIndex = idx;
        }
      }

      const newPosition = calculatePosition(targetTasks, dropIndex);

      // If nothing changed, skip
      if (sourceStatusId === targetStatusId && draggedTask.position === newPosition) {
        return;
      }

      await moveTask(draggedTask.id, {
        status_id: targetStatusId,
        position: newPosition,
      });
    },
    [findColumnId, filteredTasksByStatus, moveTask],
  );

  const handleDragCancel = useCallback(() => {
    setActiveTask(null);
  }, []);

  // ----- New task helpers -----

  // Apply a saved view: restore its filters/sort to local state.
  const handleApplyView = useCallback((view: SavedView) => {
    const filters = view.filters ?? {};
    setSearchQuery((filters.search as string) ?? "");
    setPriorityFilter((filters.priority as string) ?? "all");
    setAssigneeFilter((filters.assignee as string) ?? "all");
    setCustomFieldFilters((filters.custom_fields as Record<string, unknown>) ?? {});
  }, []);

  const openCreateDialog = useCallback((statusId?: string) => {
    setDialogStatusId(statusId);
    setDialogOpen(true);
  }, []);

  const handleTaskClick = useCallback(
    (task: Task) => {
      navigate(`/w/${wsSlug}/p/${projectSlug}/t/${task.id}`);
    },
    [navigate, wsSlug, projectSlug],
  );

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
    <div className="space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Columns3 className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">
            {currentProject.name}
          </h1>
        </div>
        <div className="flex items-center gap-3">
          {/* View toggle */}
          <div className="flex items-center gap-1 rounded-lg border border-border bg-muted/50 p-1">
            <Button
              variant="secondary"
              size="sm"
              className="h-7 gap-1.5 px-3 text-xs"
            >
              <Columns3 className="h-3.5 w-3.5" />
              Board
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1.5 px-3 text-xs"
              onClick={() =>
                navigate(`/w/${wsSlug}/p/${projectSlug}/list`)
              }
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
          <Button onClick={() => openCreateDialog()}>
            <Plus className="h-4 w-4" />
            New Task
          </Button>
        </div>
      </div>

      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-64">
          <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="Search tasks..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="pl-8"
          />
        </div>

        <Select
          value={priorityFilter}
          onChange={(e) => setPriorityFilter(e.target.value)}
          className="w-36"
        >
          <option value="all">All Priorities</option>
          <option value="urgent">Urgent</option>
          <option value="high">High</option>
          <option value="medium">Medium</option>
          <option value="low">Low</option>
          <option value="none">None</option>
        </Select>

        <Select
          value={assigneeFilter}
          onChange={(e) => setAssigneeFilter(e.target.value)}
          className="w-40"
        >
          <option value="all">All Assignees</option>
          <option value="user">User</option>
          <option value="agent">Agent</option>
          <option value="unassigned">Unassigned</option>
        </Select>

        {/* Custom field filters */}
        {filterableFields.length > 0 && (
          <DropdownMenu>
            <DropdownMenuTrigger>
              <Button variant="outline" size="sm" className="h-9 gap-1.5 text-xs">
                <SlidersHorizontal className="h-3.5 w-3.5" />
                Custom Filters
                {Object.values(customFieldFilters).some(
                  (v) => v != null && v !== "" && v !== "all",
                ) && (
                  <Badge variant="secondary" className="ml-1 h-4 px-1 text-[10px]">
                    {
                      Object.values(customFieldFilters).filter(
                        (v) => v != null && v !== "" && v !== "all",
                      ).length
                    }
                  </Badge>
                )}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent className="w-56 p-2" align="start">
              <DropdownMenuLabel>Filter by Custom Fields</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <div className="space-y-3 p-1" onClick={(e) => e.stopPropagation()}>
                {filterableFields.map((field) => (
                  <div key={field.id} className="space-y-1">
                    <label className="text-xs font-medium text-muted-foreground">
                      {field.name}
                    </label>
                    {field.field_type === "select" && (
                      <Select
                        value={
                          (customFieldFilters[field.slug] as string) ?? "all"
                        }
                        onChange={(e) => {
                          setCustomFieldFilters((prev) => ({
                            ...prev,
                            [field.slug]: e.target.value,
                          }));
                        }}
                        className="h-7 text-xs"
                      >
                        <option value="all">All</option>
                        {(
                          (field.options?.choices ?? []) as {
                            label: string;
                            value: string;
                          }[]
                        ).map((c) => (
                          <option key={c.value} value={c.value}>
                            {c.label}
                          </option>
                        ))}
                      </Select>
                    )}
                    {field.field_type === "checkbox" && (
                      <Select
                        value={
                          (customFieldFilters[field.slug] as string) ?? "all"
                        }
                        onChange={(e) => {
                          setCustomFieldFilters((prev) => ({
                            ...prev,
                            [field.slug]: e.target.value,
                          }));
                        }}
                        className="h-7 text-xs"
                      >
                        <option value="all">All</option>
                        <option value="checked">Checked</option>
                        <option value="unchecked">Unchecked</option>
                      </Select>
                    )}
                    {field.field_type === "number" && (
                      <div className="flex items-center gap-1">
                        <Input
                          type="number"
                          placeholder="Min"
                          className="h-7 w-20 text-xs"
                          value={
                            (
                              customFieldFilters[field.slug] as {
                                min?: number;
                                max?: number;
                              }
                            )?.min ?? ""
                          }
                          onChange={(e) => {
                            const prev = (customFieldFilters[field.slug] as {
                              min?: number;
                              max?: number;
                            }) ?? {};
                            setCustomFieldFilters((s) => ({
                              ...s,
                              [field.slug]: {
                                ...prev,
                                min: e.target.value
                                  ? Number(e.target.value)
                                  : undefined,
                              },
                            }));
                          }}
                        />
                        <span className="text-xs text-muted-foreground">-</span>
                        <Input
                          type="number"
                          placeholder="Max"
                          className="h-7 w-20 text-xs"
                          value={
                            (
                              customFieldFilters[field.slug] as {
                                min?: number;
                                max?: number;
                              }
                            )?.max ?? ""
                          }
                          onChange={(e) => {
                            const prev = (customFieldFilters[field.slug] as {
                              min?: number;
                              max?: number;
                            }) ?? {};
                            setCustomFieldFilters((s) => ({
                              ...s,
                              [field.slug]: {
                                ...prev,
                                max: e.target.value
                                  ? Number(e.target.value)
                                  : undefined,
                              },
                            }));
                          }}
                        />
                      </div>
                    )}
                  </div>
                ))}
                {Object.values(customFieldFilters).some(
                  (v) => v != null && v !== "" && v !== "all",
                ) && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      onClick={() => setCustomFieldFilters({})}
                      className="justify-center text-xs text-destructive"
                    >
                      Clear custom filters
                    </DropdownMenuItem>
                  </>
                )}
              </div>
            </DropdownMenuContent>
          </DropdownMenu>
        )}

        {/* Saved views */}
        {currentProject && (
          <SavedViewsMenu
            projectId={currentProject.id}
            currentViewType="board"
            currentFilters={{
              search: searchQuery,
              priority: priorityFilter,
              assignee: assigneeFilter,
              custom_fields: customFieldFilters,
            }}
            onApplyView={handleApplyView}
          />
        )}
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
            {sortedStatuses.map((status) => (
              <BoardColumn
                key={status.id}
                status={status}
                tasks={filteredTasksByStatus[status.id] ?? []}
                customFields={customFieldDefs}
                onAddTask={openCreateDialog}
                onTaskClick={handleTaskClick}
              />
            ))}

            {statuses.length === 0 && (
              <div className="flex w-full items-center justify-center py-12">
                <div className="text-center">
                  <Columns3 className="mx-auto mb-4 h-12 w-12 text-muted-foreground" />
                  <h3 className="mb-2 text-lg font-semibold">
                    No statuses configured
                  </h3>
                  <p className="text-sm text-muted-foreground">
                    Configure task statuses in project settings to use the board.
                  </p>
                </div>
              </div>
            )}
          </div>

          <DragOverlay>
            {activeTask ? (
              <div className="w-72">
                <TaskCard task={activeTask} isDragging customFields={customFieldDefs} />
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
    </div>
  );
}
