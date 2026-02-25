import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  AlertTriangle,
  ArrowRight,
  Bot,
  GitBranch,
  Info,
  Loader2,
  User,
} from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { priorityConfig } from "@/lib/utils";
import { useProjectStore } from "@/stores/project";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { Task, TaskDependency, StatusCategory } from "@/types";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const NODE_WIDTH = 220;
const NODE_HEIGHT = 88;
const COL_GAP = 100;
const ROW_GAP = 32;
const PADDING = 48;

const STATUS_CATEGORY_COLORS: Record<StatusCategory, string> = {
  backlog: "#9ca3af",
  triage: "#f59e0b",
  todo: "#60a5fa",
  in_progress: "#f59e0b",
  review: "#a78bfa",
  done: "#22c55e",
  cancelled: "#ef4444",
};

const STATUS_CATEGORY_BG: Record<StatusCategory, string> = {
  backlog: "bg-gray-50 border-gray-200 dark:bg-gray-900 dark:border-gray-700",
  triage: "bg-amber-50 border-amber-200 dark:bg-amber-950 dark:border-amber-800",
  todo: "bg-blue-50 border-blue-200 dark:bg-blue-950 dark:border-blue-800",
  in_progress:
    "bg-amber-50 border-amber-200 dark:bg-amber-950 dark:border-amber-800",
  review:
    "bg-violet-50 border-violet-200 dark:bg-violet-950 dark:border-violet-800",
  done: "bg-green-50 border-green-200 dark:bg-green-950 dark:border-green-800",
  cancelled:
    "bg-red-50 border-red-200 dark:bg-red-950 dark:border-red-800",
};

// ---------------------------------------------------------------------------
// Graph layout types
// ---------------------------------------------------------------------------

interface NodePosition {
  taskId: string;
  col: number;
  row: number;
  x: number;
  y: number;
}

// ---------------------------------------------------------------------------
// Layout algorithm: assign layers (columns) via longest-path layering.
// Only tasks that participate in "blocks" relationships are included.
// ---------------------------------------------------------------------------

function buildLayeredLayout(
  tasks: Task[],
  deps: TaskDependency[],
): { nodes: NodePosition[]; width: number; height: number } {
  const taskMap = new Map(tasks.map((t) => [t.id, t]));

  // blockedBy[taskId] = set of task IDs that block taskId
  // A "blocks" dependency: depends_on_task_id blocks task_id
  const blockedBy = new Map<string, Set<string>>();

  for (const dep of deps) {
    if (dep.dependency_type !== "blocks") continue;
    if (!blockedBy.has(dep.task_id)) blockedBy.set(dep.task_id, new Set());
    blockedBy.get(dep.task_id)!.add(dep.depends_on_task_id);
  }

  // All task IDs participating in blocking relationships
  const inGraph = new Set<string>();
  for (const dep of deps) {
    if (dep.dependency_type !== "blocks") continue;
    inGraph.add(dep.task_id);
    inGraph.add(dep.depends_on_task_id);
  }

  // Assign columns via longest-path topological layering
  const colAssignment = new Map<string, number>();

  function assignCol(taskId: string, visited: Set<string>): number {
    if (colAssignment.has(taskId)) return colAssignment.get(taskId)!;
    if (visited.has(taskId)) return 0; // cycle protection
    visited.add(taskId);

    const blockers = blockedBy.get(taskId);
    if (!blockers || blockers.size === 0) {
      colAssignment.set(taskId, 0);
      return 0;
    }

    let maxBlockerCol = 0;
    for (const blockerId of blockers) {
      if (taskMap.has(blockerId)) {
        const c = assignCol(blockerId, new Set(visited));
        maxBlockerCol = Math.max(maxBlockerCol, c + 1);
      }
    }
    colAssignment.set(taskId, maxBlockerCol);
    return maxBlockerCol;
  }

  for (const taskId of inGraph) {
    if (taskMap.has(taskId)) {
      assignCol(taskId, new Set());
    }
  }

  // Group tasks by column
  const columnGroups = new Map<number, string[]>();
  for (const [taskId, col] of colAssignment) {
    if (!columnGroups.has(col)) columnGroups.set(col, []);
    columnGroups.get(col)!.push(taskId);
  }

  // Build node positions
  const nodes: NodePosition[] = [];
  const sortedCols = Array.from(columnGroups.keys()).sort((a, b) => a - b);

  let maxRows = 0;
  for (const col of sortedCols) {
    const group = columnGroups.get(col)!;
    maxRows = Math.max(maxRows, group.length);
    const colIndex = sortedCols.indexOf(col);
    for (let row = 0; row < group.length; row++) {
      nodes.push({
        taskId: group[row]!,
        col: colIndex,
        row,
        x: PADDING + colIndex * (NODE_WIDTH + COL_GAP),
        y: PADDING + row * (NODE_HEIGHT + ROW_GAP),
      });
    }
  }

  const totalCols = sortedCols.length || 1;
  const numRows = maxRows || 1;
  const width =
    PADDING * 2 + totalCols * NODE_WIDTH + (totalCols - 1) * COL_GAP;
  const height =
    PADDING * 2 + numRows * NODE_HEIGHT + Math.max(0, numRows - 1) * ROW_GAP;

  return { nodes, width: Math.max(width, 400), height: Math.max(height, 200) };
}

// Standalone layout: grid of all tasks with no dependencies
function buildStandaloneLayout(
  tasks: Task[],
  cols = 4,
): { nodes: NodePosition[]; width: number; height: number } {
  const nodes: NodePosition[] = [];
  for (let i = 0; i < tasks.length; i++) {
    const col = i % cols;
    const row = Math.floor(i / cols);
    nodes.push({
      taskId: tasks[i]!.id,
      col,
      row,
      x: PADDING + col * (NODE_WIDTH + COL_GAP),
      y: PADDING + row * (NODE_HEIGHT + ROW_GAP),
    });
  }

  const numRows = Math.ceil(tasks.length / cols);
  const numCols = Math.min(tasks.length, cols);
  const width =
    PADDING * 2 + numCols * NODE_WIDTH + Math.max(0, numCols - 1) * COL_GAP;
  const height =
    PADDING * 2 +
    numRows * NODE_HEIGHT +
    Math.max(0, numRows - 1) * ROW_GAP;

  return { nodes, width: Math.max(width, 400), height: Math.max(height, 200) };
}

// ---------------------------------------------------------------------------
// SVG arrow between two nodes
// ---------------------------------------------------------------------------

function DependencyArrow({
  from,
  to,
}: {
  from: NodePosition;
  to: NodePosition;
}) {
  const startX = from.x + NODE_WIDTH;
  const startY = from.y + NODE_HEIGHT / 2;
  const endX = to.x;
  const endY = to.y + NODE_HEIGHT / 2;

  // Horizontal bezier with adaptive control point offset
  const cpOffset = Math.min(Math.abs(endX - startX) * 0.45, 80);
  const d = `M ${startX} ${startY} C ${startX + cpOffset} ${startY}, ${endX - cpOffset} ${endY}, ${endX} ${endY}`;

  return (
    <g>
      <path
        d={d}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        className="text-muted-foreground/50"
        markerEnd="url(#arrowhead)"
      />
    </g>
  );
}

// ---------------------------------------------------------------------------
// Task node card rendered in SVG via foreignObject
// ---------------------------------------------------------------------------

function TaskNode({
  task,
  node,
  category,
  onClick,
}: {
  task: Task;
  node: NodePosition;
  category: StatusCategory;
  onClick: () => void;
}) {
  const pConfig = priorityConfig[task.priority];
  const accentColor = STATUS_CATEGORY_COLORS[category];
  const bgClass = STATUS_CATEGORY_BG[category];

  return (
    <foreignObject
      x={node.x}
      y={node.y}
      width={NODE_WIDTH}
      height={NODE_HEIGHT}
    >
      <div
        className={cn(
          "flex h-full cursor-pointer flex-col justify-between rounded-lg border-2 px-3 py-2.5 transition-all hover:shadow-md hover:brightness-95 active:scale-[0.98]",
          bgClass,
        )}
        style={{ borderLeftColor: accentColor, borderLeftWidth: 4 }}
        onClick={onClick}
      >
        <p
          className="line-clamp-2 text-xs font-semibold leading-snug text-foreground"
          title={task.title}
        >
          {task.title}
        </p>
        <div className="mt-1 flex items-center justify-between gap-1">
          <div className="flex items-center gap-1">
            {task.priority !== "none" && (
              <Badge
                variant="secondary"
                className={cn("h-4 px-1 text-[9px]", pConfig.color)}
              >
                {pConfig.label}
              </Badge>
            )}
          </div>
          <div className="flex items-center gap-1">
            {task.assignee_type === "agent" ? (
              <Bot className="h-3 w-3 text-violet-500" />
            ) : task.assignee_type === "user" ? (
              <User className="h-3 w-3 text-sky-500" />
            ) : null}
          </div>
        </div>
      </div>
    </foreignObject>
  );
}

// ---------------------------------------------------------------------------
// Batch API response type
// ---------------------------------------------------------------------------

interface DependencyGraphResponse {
  tasks: Task[];
  dependencies: TaskDependency[];
}

// ---------------------------------------------------------------------------
// Timeline page component
// ---------------------------------------------------------------------------

export function TimelinePage() {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const { currentProject, statuses, fetchStatuses } = useProjectStore();

  const [graphTasks, setGraphTasks] = useState<Task[]>([]);
  const [deps, setDeps] = useState<TaskDependency[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // When tasks exist but no blocking deps, allow user to show all tasks anyway
  const [showAllTasks, setShowAllTasks] = useState(false);
  const svgContainerRef = useRef<HTMLDivElement>(null);

  // Fetch statuses
  useEffect(() => {
    if (currentProject) {
      fetchStatuses(currentProject.id);
    }
  }, [currentProject, fetchStatuses]);

  // Fetch tasks + dependencies in a single batch call
  useEffect(() => {
    if (!currentProject) return;
    setLoading(true);
    setError(null);
    setShowAllTasks(false);
    api<DependencyGraphResponse>(
      `/api/v1/projects/${currentProject.id}/dependency-graph`,
    )
      .then((result) => {
        setGraphTasks(result.tasks ?? []);
        setDeps(result.dependencies ?? []);
      })
      .catch(() => {
        setError("Failed to load dependency graph");
      })
      .finally(() => {
        setLoading(false);
      });
  }, [currentProject]);

  // Build status category lookup
  const statusCategoryMap = useMemo(() => {
    const map = new Map<string, StatusCategory>();
    for (const s of statuses) {
      map.set(s.id, s.category);
    }
    return map;
  }, [statuses]);

  // Blocking deps only (edges rendered as arrows)
  const blockingDeps = useMemo(
    () => deps.filter((d) => d.dependency_type === "blocks"),
    [deps],
  );

  // Tasks that participate in at least one "blocks" dependency
  const connectedTaskIds = useMemo(() => {
    const ids = new Set<string>();
    for (const d of blockingDeps) {
      ids.add(d.task_id);
      ids.add(d.depends_on_task_id);
    }
    return ids;
  }, [blockingDeps]);

  const hasDependencies = blockingDeps.length > 0;

  // Tasks to use for DAG layout (only connected ones)
  const dagTasks = useMemo(
    () => graphTasks.filter((t) => connectedTaskIds.has(t.id)),
    [graphTasks, connectedTaskIds],
  );

  // Build the DAG layout (only connected tasks)
  const dagLayout = useMemo(() => {
    if (dagTasks.length === 0) return null;
    return buildLayeredLayout(dagTasks, blockingDeps);
  }, [dagTasks, blockingDeps]);

  // Build standalone grid layout (all tasks)
  const standaloneLayout = useMemo(() => {
    if (graphTasks.length === 0) return null;
    return buildStandaloneLayout(graphTasks);
  }, [graphTasks]);

  // Build lookup maps for the active layout
  const activeLayout = hasDependencies
    ? dagLayout
    : showAllTasks
      ? standaloneLayout
      : null;

  const taskMap = useMemo(
    () => new Map(graphTasks.map((t) => [t.id, t])),
    [graphTasks],
  );

  const nodeMap = useMemo(() => {
    if (!activeLayout) return new Map<string, NodePosition>();
    return new Map(activeLayout.nodes.map((n) => [n.taskId, n]));
  }, [activeLayout]);

  // Edges that have both endpoints in the layout
  const visibleEdges = useMemo(() => {
    if (!hasDependencies) return [];
    return blockingDeps.filter(
      (d) => nodeMap.has(d.task_id) && nodeMap.has(d.depends_on_task_id),
    );
  }, [blockingDeps, hasDependencies, nodeMap]);

  const handleTaskClick = useCallback(
    (taskId: string) => {
      navigate(`/w/${wsSlug}/p/${projectSlug}/t/${taskId}`);
    },
    [navigate, wsSlug, projectSlug],
  );

  // ---------------------------------------------------------------------------
  // Render states
  // ---------------------------------------------------------------------------

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
      <div className="flex items-center gap-3">
        <GitBranch className="h-5 w-5 text-muted-foreground" />
        <h1 className="text-2xl font-bold tracking-tight">
          {currentProject.name}
        </h1>
      </div>

      {/* Error */}
      {error && (
        <div className="flex items-center gap-2 rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-400">
          <AlertTriangle className="h-4 w-4 shrink-0" />
          {error}
        </div>
      )}

      {/* Loading */}
      {loading ? (
        <div className="flex items-center justify-center py-20">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          <span className="ml-2 text-sm text-muted-foreground">
            Loading dependency graph...
          </span>
        </div>
      ) : graphTasks.length === 0 ? (
        /* No tasks at all */
        <div className="flex flex-col items-center justify-center py-20">
          <GitBranch className="mb-4 h-12 w-12 text-muted-foreground/50" />
          <h3 className="mb-2 text-lg font-semibold">No tasks yet</h3>
          <p className="max-w-sm text-center text-sm text-muted-foreground">
            Create tasks and add blocking dependencies between them to see the
            dependency graph.
          </p>
        </div>
      ) : !hasDependencies && !showAllTasks ? (
        /* Tasks exist but no blocking dependencies */
        <div className="flex flex-col items-center justify-center py-20">
          <Info className="mb-4 h-12 w-12 text-muted-foreground/50" />
          <h3 className="mb-2 text-lg font-semibold">No dependencies found</h3>
          <p className="mb-6 max-w-sm text-center text-sm text-muted-foreground">
            Add blocking dependencies between tasks to see the dependency graph.
            You can set a dependency when editing a task.
          </p>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setShowAllTasks(true)}
          >
            Show all {graphTasks.length} tasks anyway
          </Button>
        </div>
      ) : activeLayout ? (
        <>
          {/* Legend */}
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
            <span className="font-medium">Legend:</span>
            {(
              [
                ["backlog", "Backlog"],
                ["todo", "To Do"],
                ["in_progress", "In Progress"],
                ["review", "Review"],
                ["done", "Done"],
                ["cancelled", "Cancelled"],
              ] as [StatusCategory, string][]
            ).map(([cat, label]) => (
              <span key={cat} className="flex items-center gap-1">
                <span
                  className="inline-block h-2.5 w-2.5 rounded-full"
                  style={{ backgroundColor: STATUS_CATEGORY_COLORS[cat] }}
                />
                {label}
              </span>
            ))}
            {hasDependencies && (
              <span className="flex items-center gap-1">
                <ArrowRight className="h-3 w-3" />
                blocks
              </span>
            )}
          </div>

          {/* "showing all tasks" notice */}
          {!hasDependencies && showAllTasks && (
            <div className="flex items-center gap-2 rounded-lg border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-700 dark:border-blue-800 dark:bg-blue-950 dark:text-blue-400">
              <Info className="h-3.5 w-3.5 shrink-0" />
              No blocking dependencies — showing all tasks as a grid. Add
              dependencies in task details to see the graph.
              <button
                className="ml-auto shrink-0 underline hover:no-underline"
                onClick={() => setShowAllTasks(false)}
              >
                Hide
              </button>
            </div>
          )}

          {/* DAG SVG */}
          <div
            ref={svgContainerRef}
            className="overflow-auto rounded-xl border border-border bg-muted/20 p-0"
            style={{ minHeight: 200 }}
          >
            <svg
              width={activeLayout.width}
              height={activeLayout.height}
              className="block"
            >
              <defs>
                <marker
                  id="arrowhead"
                  markerWidth="10"
                  markerHeight="7"
                  refX="9"
                  refY="3.5"
                  orient="auto"
                >
                  <polygon
                    points="0 0, 10 3.5, 0 7"
                    className="fill-muted-foreground/50"
                  />
                </marker>
              </defs>

              {/* Arrows */}
              {visibleEdges.map((dep) => {
                const fromNode = nodeMap.get(dep.depends_on_task_id);
                const toNode = nodeMap.get(dep.task_id);
                if (!fromNode || !toNode) return null;
                return (
                  <DependencyArrow
                    key={dep.id}
                    from={fromNode}
                    to={toNode}
                  />
                );
              })}

              {/* Task nodes */}
              {activeLayout.nodes.map((node) => {
                const task = taskMap.get(node.taskId);
                if (!task) return null;
                const category =
                  statusCategoryMap.get(task.status_id) ?? "backlog";
                return (
                  <TaskNode
                    key={node.taskId}
                    task={task}
                    node={node}
                    category={category}
                    onClick={() => handleTaskClick(node.taskId)}
                  />
                );
              })}
            </svg>
          </div>

          {/* Stats footer */}
          <div className="flex flex-wrap gap-6 text-xs text-muted-foreground">
            <span>
              {activeLayout.nodes.length} task
              {activeLayout.nodes.length !== 1 ? "s" : ""} shown
            </span>
            {hasDependencies && (
              <>
                <span>
                  {visibleEdges.length} blocking relationship
                  {visibleEdges.length !== 1 ? "s" : ""}
                </span>
                {graphTasks.length - dagTasks.length > 0 && (
                  <span>
                    {graphTasks.length - dagTasks.length} standalone (not shown)
                  </span>
                )}
              </>
            )}
          </div>
        </>
      ) : null}
    </div>
  );
}
