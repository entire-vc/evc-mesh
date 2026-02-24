import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  AlertTriangle,
  ArrowRight,
  Bot,
  Columns3,
  GitBranch,
  List,
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
const NODE_HEIGHT = 80;
const COL_GAP = 80;
const ROW_GAP = 24;
const PADDING = 40;

const STATUS_CATEGORY_COLORS: Record<StatusCategory, string> = {
  backlog: "#9ca3af",
  todo: "#9ca3af",
  in_progress: "#3b82f6",
  review: "#f59e0b",
  done: "#22c55e",
  cancelled: "#ef4444",
};

const STATUS_CATEGORY_BG: Record<StatusCategory, string> = {
  backlog: "bg-gray-50 border-gray-300 dark:bg-gray-900 dark:border-gray-700",
  todo: "bg-gray-50 border-gray-300 dark:bg-gray-900 dark:border-gray-700",
  in_progress:
    "bg-blue-50 border-blue-300 dark:bg-blue-950 dark:border-blue-800",
  review:
    "bg-amber-50 border-amber-300 dark:bg-amber-950 dark:border-amber-800",
  done: "bg-green-50 border-green-300 dark:bg-green-950 dark:border-green-800",
  cancelled:
    "bg-red-50 border-red-300 dark:bg-red-950 dark:border-red-800",
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
// Layout algorithm: assign layers (columns) via longest-path layering
// ---------------------------------------------------------------------------

function buildLayeredLayout(
  tasks: Task[],
  deps: TaskDependency[],
): { nodes: NodePosition[]; width: number; height: number } {
  const taskMap = new Map(tasks.map((t) => [t.id, t]));

  // Build adjacency: blockedBy[taskId] = set of tasks that block it
  // A dependency with type "blocks" means: depends_on_task blocks task_id
  // So task_id is blocked by depends_on_task_id
  const blockedBy = new Map<string, Set<string>>();
  const blocks = new Map<string, Set<string>>();

  for (const dep of deps) {
    if (dep.dependency_type !== "blocks") continue;
    // task_id is blocked by depends_on_task_id
    if (!blockedBy.has(dep.task_id)) blockedBy.set(dep.task_id, new Set());
    blockedBy.get(dep.task_id)!.add(dep.depends_on_task_id);

    if (!blocks.has(dep.depends_on_task_id))
      blocks.set(dep.depends_on_task_id, new Set());
    blocks.get(dep.depends_on_task_id)!.add(dep.task_id);
  }

  // All task IDs participating in blocking relationships
  const inGraph = new Set<string>();
  for (const dep of deps) {
    if (dep.dependency_type !== "blocks") continue;
    inGraph.add(dep.task_id);
    inGraph.add(dep.depends_on_task_id);
  }

  // Assign columns via topological order (longest path)
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
        const c = assignCol(blockerId, visited);
        maxBlockerCol = Math.max(maxBlockerCol, c + 1);
      }
    }
    colAssignment.set(taskId, maxBlockerCol);
    return maxBlockerCol;
  }

  // Assign columns for tasks in blocking graph
  for (const taskId of inGraph) {
    if (taskMap.has(taskId)) {
      assignCol(taskId, new Set());
    }
  }

  // Standalone tasks (not in any blocking relationship)
  const standaloneIds = tasks
    .filter((t) => !inGraph.has(t.id))
    .map((t) => t.id);

  // Group tasks by column
  const columnGroups = new Map<number, string[]>();
  for (const [taskId, col] of colAssignment) {
    if (!columnGroups.has(col)) columnGroups.set(col, []);
    columnGroups.get(col)!.push(taskId);
  }

  // Find max column used in blocking graph
  const maxCol =
    columnGroups.size > 0
      ? Math.max(...Array.from(columnGroups.keys()))
      : -1;

  // Place standalone tasks in a separate column at the end
  const standaloneCol = maxCol + 1;
  if (standaloneIds.length > 0) {
    columnGroups.set(standaloneCol, standaloneIds);
    for (const id of standaloneIds) {
      colAssignment.set(id, standaloneCol);
    }
  }

  // Build node positions
  const nodes: NodePosition[] = [];
  const sortedCols = Array.from(columnGroups.keys()).sort((a, b) => a - b);

  let maxRows = 0;
  for (const col of sortedCols) {
    const group = columnGroups.get(col)!;
    maxRows = Math.max(maxRows, group.length);
    for (let row = 0; row < group.length; row++) {
      const colIndex = sortedCols.indexOf(col);
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
  const width = PADDING * 2 + totalCols * NODE_WIDTH + (totalCols - 1) * COL_GAP;
  const height = PADDING * 2 + maxRows * NODE_HEIGHT + (maxRows - 1) * ROW_GAP;

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

  // Cubic bezier for a smooth horizontal arrow
  const cpOffset = Math.min(Math.abs(endX - startX) * 0.4, 60);
  const path = `M ${startX} ${startY} C ${startX + cpOffset} ${startY}, ${endX - cpOffset} ${endY}, ${endX} ${endY}`;

  return (
    <g>
      <path
        d={path}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        className="text-muted-foreground/40"
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
  const borderColor = STATUS_CATEGORY_COLORS[category];
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
          "flex h-full cursor-pointer flex-col justify-between rounded-lg border-2 p-2.5 transition-shadow hover:shadow-md",
          bgClass,
        )}
        style={{ borderLeftColor: borderColor, borderLeftWidth: 3 }}
        onClick={onClick}
      >
        <p className="truncate text-xs font-semibold leading-tight text-foreground">
          {task.title}
        </p>
        <div className="flex items-center justify-between gap-1">
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

  // Build the layout
  const layout = useMemo(() => {
    if (graphTasks.length === 0) return null;
    return buildLayeredLayout(graphTasks, deps);
  }, [graphTasks, deps]);

  // Build lookup maps
  const taskMap = useMemo(
    () => new Map(graphTasks.map((t) => [t.id, t])),
    [graphTasks],
  );
  const nodeMap = useMemo(() => {
    if (!layout) return new Map<string, NodePosition>();
    return new Map(layout.nodes.map((n) => [n.taskId, n]));
  }, [layout]);

  // Blocking edges for arrows
  const blockingEdges = useMemo(() => {
    return deps.filter(
      (d) =>
        d.dependency_type === "blocks" &&
        nodeMap.has(d.task_id) &&
        nodeMap.has(d.depends_on_task_id),
    );
  }, [deps, nodeMap]);

  const handleTaskClick = useCallback(
    (taskId: string) => {
      navigate(`/w/${wsSlug}/p/${projectSlug}/t/${taskId}`);
    },
    [navigate, wsSlug, projectSlug],
  );

  // Render states
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
          <GitBranch className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">
            {currentProject.name}
          </h1>
        </div>

        {/* View toggle */}
        <div className="flex items-center gap-1 rounded-lg border border-border bg-muted/50 p-1">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-3 text-xs"
            onClick={() =>
              navigate(`/w/${wsSlug}/p/${projectSlug}`)
            }
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
            variant="secondary"
            size="sm"
            className="h-7 gap-1.5 px-3 text-xs"
          >
            <GitBranch className="h-3.5 w-3.5" />
            Timeline
          </Button>
        </div>
      </div>

      {/* Error */}
      {error && (
        <div className="flex items-center gap-2 rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-400">
          <AlertTriangle className="h-4 w-4" />
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
        <div className="flex flex-col items-center justify-center py-20">
          <GitBranch className="mb-4 h-12 w-12 text-muted-foreground" />
          <h3 className="mb-2 text-lg font-semibold">No tasks yet</h3>
          <p className="text-sm text-muted-foreground">
            Create tasks and add dependencies to see the timeline graph.
          </p>
        </div>
      ) : layout && layout.nodes.length > 0 ? (
        <>
          {/* Legend */}
          <div className="flex flex-wrap items-center gap-4 text-xs text-muted-foreground">
            <span className="font-medium">Legend:</span>
            <span className="flex items-center gap-1">
              <span className="inline-block h-2.5 w-2.5 rounded-full bg-gray-400" />
              Backlog/To Do
            </span>
            <span className="flex items-center gap-1">
              <span className="inline-block h-2.5 w-2.5 rounded-full bg-blue-500" />
              In Progress
            </span>
            <span className="flex items-center gap-1">
              <span className="inline-block h-2.5 w-2.5 rounded-full bg-amber-500" />
              Review
            </span>
            <span className="flex items-center gap-1">
              <span className="inline-block h-2.5 w-2.5 rounded-full bg-green-500" />
              Done
            </span>
            <span className="flex items-center gap-1">
              <ArrowRight className="h-3 w-3" />
              blocks
            </span>
          </div>

          {/* DAG SVG */}
          <div
            ref={svgContainerRef}
            className="overflow-auto rounded-xl border border-border bg-muted/30 p-0"
          >
            <svg
              width={layout.width}
              height={layout.height}
              className="block"
            >
              <defs>
                <marker
                  id="arrowhead"
                  markerWidth="8"
                  markerHeight="6"
                  refX="8"
                  refY="3"
                  orient="auto"
                >
                  <polygon
                    points="0 0, 8 3, 0 6"
                    className="fill-muted-foreground/40"
                  />
                </marker>
              </defs>

              {/* Arrows */}
              {blockingEdges.map((dep) => {
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
              {layout.nodes.map((node) => {
                const task = taskMap.get(node.taskId);
                if (!task) return null;
                const category =
                  statusCategoryMap.get(task.status_id) ?? "todo";
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

          {/* Stats */}
          <div className="flex gap-6 text-xs text-muted-foreground">
            <span>
              {graphTasks.length} task{graphTasks.length !== 1 ? "s" : ""}
            </span>
            <span>
              {blockingEdges.length} blocking relationship
              {blockingEdges.length !== 1 ? "s" : ""}
            </span>
            <span>
              {
                graphTasks.filter(
                  (t) =>
                    !deps.some(
                      (d) =>
                        d.dependency_type === "blocks" &&
                        (d.task_id === t.id ||
                          d.depends_on_task_id === t.id),
                    ),
                ).length
              }{" "}
              standalone
            </span>
          </div>
        </>
      ) : (
        <div className="flex flex-col items-center justify-center py-20">
          <GitBranch className="mb-4 h-12 w-12 text-muted-foreground" />
          <h3 className="mb-2 text-lg font-semibold">
            No dependency graph to display
          </h3>
          <p className="text-sm text-muted-foreground">
            Add blocking dependencies between tasks to see the timeline.
          </p>
        </div>
      )}
    </div>
  );
}
