import { useCallback, useEffect, useState } from "react";
import { Inbox, ArrowRight } from "lucide-react";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import type { PaginatedResponse, Task, TaskStatus } from "@/types";

const PRIORITY_COLORS = {
  urgent: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400",
  high: "bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400",
  medium: "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400",
  low: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400",
  none: "bg-gray-100 text-gray-600 dark:bg-gray-800/50 dark:text-gray-400",
};

// MoveDialog lets the user pick a target project + status to move a triage task into.
function MoveDialog({
  task,
  open,
  onOpenChange,
  onMoved,
}: {
  task: Task | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onMoved: (taskId: string) => void;
}) {
  const { projects } = useProjectStore();

  const [selectedProjectId, setSelectedProjectId] = useState("");
  const [statuses, setStatuses] = useState<TaskStatus[]>([]);
  const [selectedStatusId, setSelectedStatusId] = useState("");
  const [isLoadingStatuses, setIsLoadingStatuses] = useState(false);
  const [isMoving, setIsMoving] = useState(false);

  // Reset when dialog opens / task changes.
  useEffect(() => {
    if (open && task) {
      setSelectedProjectId(task.project_id);
      setSelectedStatusId("");
      setStatuses([]);
    }
  }, [open, task]);

  // Fetch statuses whenever the selected project changes.
  useEffect(() => {
    if (!selectedProjectId) {
      setStatuses([]);
      setSelectedStatusId("");
      return;
    }
    setIsLoadingStatuses(true);
    api<TaskStatus[]>(`/api/v1/projects/${selectedProjectId}/statuses`)
      .then((list) => {
        // Exclude triage category — we are moving OUT of triage.
        const nonTriage = (list ?? []).filter((s) => s.category !== "triage");
        setStatuses(nonTriage);
        // Pre-select the first "todo" status, or fall back to the first available.
        const defaultStatus =
          nonTriage.find((s) => s.category === "todo") ?? nonTriage[0];
        setSelectedStatusId(defaultStatus?.id ?? "");
      })
      .catch(() => {
        setStatuses([]);
        setSelectedStatusId("");
      })
      .finally(() => setIsLoadingStatuses(false));
  }, [selectedProjectId]);

  const handleMove = useCallback(async () => {
    if (!task || !selectedStatusId) return;
    setIsMoving(true);
    try {
      await api(`/api/v1/tasks/${task.id}/move`, {
        method: "POST",
        body: { status_id: selectedStatusId },
      });
      const statusName =
        statuses.find((s) => s.id === selectedStatusId)?.name ?? "new status";
      toast.success(`Moved to "${statusName}"`);
      onMoved(task.id);
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to move task");
    } finally {
      setIsMoving(false);
    }
  }, [task, selectedStatusId, statuses, onMoved, onOpenChange]);

  if (!task) return null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent onClose={() => onOpenChange(false)}>
        <DialogHeader>
          <DialogTitle>Move Task</DialogTitle>
          <p className="mt-1 line-clamp-2 text-sm text-muted-foreground">
            {task.title}
          </p>
        </DialogHeader>

        <div className="mt-4 space-y-4">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">Target Project</label>
            <Select
              value={selectedProjectId}
              onChange={(e) => setSelectedProjectId(e.target.value)}
            >
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          </div>

          <div className="space-y-1.5">
            <label className="text-sm font-medium">Target Status</label>
            {isLoadingStatuses ? (
              <div className="h-9 animate-pulse rounded-md border border-border bg-muted" />
            ) : (
              <Select
                value={selectedStatusId}
                onChange={(e) => setSelectedStatusId(e.target.value)}
                disabled={statuses.length === 0}
              >
                {statuses.length === 0 ? (
                  <option value="">No statuses available</option>
                ) : (
                  statuses.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name} ({s.category})
                    </option>
                  ))
                )}
              </Select>
            )}
          </div>
        </div>

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={isMoving}
          >
            Cancel
          </Button>
          <Button
            onClick={handleMove}
            disabled={isMoving || !selectedStatusId || isLoadingStatuses}
          >
            {isMoving ? "Moving..." : "Move Task"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TriageTaskRow({
  task,
  projectName,
  onMove,
}: {
  task: Task;
  projectName: string;
  onMove: (task: Task) => void;
}) {
  const priorityColor =
    PRIORITY_COLORS[task.priority] ?? PRIORITY_COLORS.none;
  const createdDate = new Date(task.created_at).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
  });

  return (
    <Card>
      <CardContent className="flex items-center gap-3 py-3">
        <div className="min-w-0 flex-1">
          <div className="mb-0.5 flex items-center gap-2">
            <span className="text-xs text-muted-foreground">{projectName}</span>
            <span className="text-xs text-muted-foreground">·</span>
            <span className="text-xs text-muted-foreground">{createdDate}</span>
          </div>
          <p className="truncate text-sm font-medium">{task.title}</p>
          {task.description && (
            <p className="mt-0.5 truncate text-xs text-muted-foreground">
              {task.description}
            </p>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span
            className={`rounded-full px-2 py-0.5 text-xs font-medium ${priorityColor}`}
          >
            {task.priority}
          </span>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1 text-xs"
            onClick={() => onMove(task)}
          >
            Move
            <ArrowRight className="h-3 w-3" />
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

export function TriagePage() {
  const { currentWorkspace } = useWorkspaceStore();
  const { projects, fetchProjects } = useProjectStore();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [total, setTotal] = useState(0);

  // Move dialog state.
  const [moveTask, setMoveTask] = useState<Task | null>(null);
  const [moveDialogOpen, setMoveDialogOpen] = useState(false);

  const fetchTriage = useCallback(async (workspaceId: string) => {
    setIsLoading(true);
    try {
      const result = await api<PaginatedResponse<Task>>(
        `/api/v1/workspaces/${workspaceId}/triage`,
        { params: { per_page: 50 } },
      );
      setTasks(result.items ?? []);
      setTotal(result.total_count ?? result.total ?? 0);
    } catch {
      // Triage may be empty if no triage statuses exist — treat gracefully.
      setTasks([]);
      setTotal(0);
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (currentWorkspace) {
      fetchTriage(currentWorkspace.id);
      if (projects.length === 0) {
        fetchProjects(currentWorkspace.id);
      }
    }
  }, [currentWorkspace, fetchTriage, fetchProjects, projects.length]);

  const projectMap = Object.fromEntries(projects.map((p) => [p.id, p.name]));

  const handleMoveClick = useCallback((task: Task) => {
    setMoveTask(task);
    setMoveDialogOpen(true);
  }, []);

  // Remove the task from the triage list once it has been successfully moved.
  const handleMoved = useCallback((taskId: string) => {
    setTasks((prev) => prev.filter((t) => t.id !== taskId));
    setTotal((prev) => Math.max(0, prev - 1));
  }, []);

  return (
    <div className="space-y-6">
      {total > 0 && (
        <p className="text-sm text-muted-foreground">
          {total} task{total !== 1 ? "s" : ""} awaiting triage
        </p>
      )}

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Card key={i}>
              <CardContent className="py-3">
                <Skeleton className="h-4 w-64" />
                <Skeleton className="mt-1.5 h-3 w-40" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : tasks.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <Inbox className="mb-4 h-12 w-12 text-muted-foreground" />
            <h3 className="mb-2 text-lg font-semibold">Inbox is empty</h3>
            <p className="text-sm text-muted-foreground">
              Tasks assigned to a "triage" status category will appear here.
            </p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-2">
          {tasks.map((task) => (
            <TriageTaskRow
              key={task.id}
              task={task}
              projectName={projectMap[task.project_id] ?? "Unknown Project"}
              onMove={handleMoveClick}
            />
          ))}
        </div>
      )}

      <MoveDialog
        task={moveTask}
        open={moveDialogOpen}
        onOpenChange={setMoveDialogOpen}
        onMoved={handleMoved}
      />
    </div>
  );
}
