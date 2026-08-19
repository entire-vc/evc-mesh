import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { ListTree, Loader2, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { priorityConfig } from "@/lib/utils";
import { useProjectStore } from "@/stores/project";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import type { Task } from "@/types";

interface SubtaskListProps {
  taskId: string;
  onOpenSubtask?: (subtaskId: string) => void;
  /** Bump this to force a refetch — e.g. after a Dependencies-tab change that
   *  may have added or removed a subtask via an is_child_of edge. */
  refreshKey?: number;
}

export function SubtaskList({ taskId, onOpenSubtask, refreshKey }: SubtaskListProps) {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const { statuses, projects } = useProjectStore();
  const [subtasks, setSubtasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [title, setTitle] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
  }, [fetchSubtasks, refreshKey]);

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = title.trim();
    if (!trimmed) {
      setError("Title is required.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const created = await api<Task>(`/api/v1/tasks/${taskId}/subtasks`, {
        method: "POST",
        body: { title: trimmed },
      });
      setSubtasks((prev) => [...prev, created]);
      setTitle("");
      setShowForm(false);
    } catch (err: unknown) {
      setError(
        err instanceof Error ? err.message : "Failed to add subtask.",
      );
    } finally {
      setSubmitting(false);
    }
  };

  const addSubtaskForm = showForm && (
    <form
      onSubmit={(e) => void handleAdd(e)}
      className="space-y-2 rounded-lg border border-border bg-muted/20 p-3"
    >
      <Input
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Subtask title"
        className="h-8 text-sm"
        autoFocus
      />
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div className="flex gap-2">
        <Button
          type="submit"
          size="sm"
          className="flex-1"
          disabled={submitting || !title.trim()}
        >
          {submitting ? (
            <>
              <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
              Adding...
            </>
          ) : (
            "Add Subtask"
          )}
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            setShowForm(false);
            setError(null);
            setTitle("");
          }}
        >
          Cancel
        </Button>
      </div>
    </form>
  );

  const addSubtaskButton = (
    <Button
      variant="ghost"
      size="sm"
      className="h-7 gap-1 text-xs"
      onClick={() => {
        setShowForm((v) => !v);
        setError(null);
      }}
    >
      <Plus className="h-3 w-3" />
      Add subtask
    </Button>
  );

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
      <div className="space-y-3">
        <div className="flex justify-end">{addSubtaskButton}</div>
        {addSubtaskForm}
        <div className="flex flex-col items-center py-8 text-muted-foreground">
          <ListTree className="mb-2 h-8 w-8" />
          <p className="text-sm">No subtasks.</p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <div className="flex justify-end">{addSubtaskButton}</div>
      {addSubtaskForm}
      {subtasks.map((subtask) => {
        const pConfig = priorityConfig[subtask.priority];
        const status = statuses.find((s) => s.id === subtask.status_id);

        return (
          <div
            key={subtask.id}
            className="flex cursor-pointer items-center justify-between rounded-lg border border-border p-3 transition-colors hover:bg-muted/50"
            onClick={() => {
              if (onOpenSubtask) {
                onOpenSubtask(subtask.id);
              } else {
                const resolvedProject = projects.find(
                  (p) => p.id === subtask.project_id,
                );
                const slug = resolvedProject?.slug ?? projectSlug;
                navigate(`/w/${wsSlug}/p/${slug}/t/${subtask.id}`);
              }
            }}
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
