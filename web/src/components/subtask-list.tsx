import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ListTree } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { priorityConfig } from "@/lib/utils";
import { useProjectStore } from "@/stores/project";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import type { Task } from "@/types";

interface SubtaskListProps {
  taskId: string;
}

export function SubtaskList({ taskId }: SubtaskListProps) {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const { statuses } = useProjectStore();
  const [subtasks, setSubtasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);

  const fetchSubtasks = useCallback(async () => {
    try {
      const data = await api<{ items: Task[] }>(
        `/api/v1/tasks/${taskId}/subtasks`,
      );
      setSubtasks(data.items ?? []);
    } catch {
      // silently fail
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  useEffect(() => {
    void fetchSubtasks();
  }, [fetchSubtasks]);

  if (loading) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-14 w-full" />
        <Skeleton className="h-14 w-full" />
      </div>
    );
  }

  if (subtasks.length === 0) {
    return (
      <div className="flex flex-col items-center py-8 text-muted-foreground">
        <ListTree className="mb-2 h-8 w-8" />
        <p className="text-sm">No subtasks.</p>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {subtasks.map((subtask) => {
        const pConfig = priorityConfig[subtask.priority];
        const status = statuses.find((s) => s.id === subtask.status_id);

        return (
          <div
            key={subtask.id}
            className="flex cursor-pointer items-center justify-between rounded-lg border border-border p-3 transition-colors hover:bg-muted/50"
            onClick={() =>
              navigate(`/w/${wsSlug}/p/${projectSlug}/t/${subtask.id}`)
            }
          >
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{subtask.title}</p>
            </div>
            <div className="flex shrink-0 items-center gap-2 ml-3">
              {status && (
                <Badge
                  variant="secondary"
                  className="gap-1 text-[10px]"
                >
                  <span
                    className="inline-block h-2 w-2 rounded-full"
                    style={{ backgroundColor: status.color }}
                  />
                  {status.name}
                </Badge>
              )}
              {subtask.priority !== "none" && (
                <Badge
                  variant="outline"
                  className={cn("text-[10px]", pConfig.color)}
                >
                  {pConfig.label}
                </Badge>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
