import { useCallback, useEffect, useState } from "react";
import { Inbox, ArrowRight } from "lucide-react";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import type { PaginatedResponse, Task } from "@/types";

const PRIORITY_COLORS = {
  urgent: "bg-red-100 text-red-700",
  high: "bg-orange-100 text-orange-700",
  medium: "bg-amber-100 text-amber-700",
  low: "bg-blue-100 text-blue-700",
  none: "bg-gray-100 text-gray-600",
};

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
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-0.5">
            <span className="text-xs text-muted-foreground">{projectName}</span>
            <span className="text-xs text-muted-foreground">·</span>
            <span className="text-xs text-muted-foreground">{createdDate}</span>
          </div>
          <p className="text-sm font-medium truncate">{task.title}</p>
          {task.description && (
            <p className="text-xs text-muted-foreground truncate mt-0.5">
              {task.description}
            </p>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0">
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

  const handleMove = useCallback(
    async (task: Task) => {
      // Quick action: navigate to the task so the user can explicitly set status/priority.
      // A more advanced implementation would show a picker dialog.
      toast.info(`Open task "${task.title}" to change its status and route it.`);
    },
    [],
  );

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
                <Skeleton className="h-3 w-40 mt-1.5" />
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
              onMove={handleMove}
            />
          ))}
        </div>
      )}
    </div>
  );
}
