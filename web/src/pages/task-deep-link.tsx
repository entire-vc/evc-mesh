import { useEffect, useState } from "react";
import { Navigate, useParams, useLocation } from "react-router";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { api } from "@/lib/api";
import { Skeleton } from "@/components/ui/skeleton";
import type { PaginatedResponse, Project, Task } from "@/types";

export function TaskDeepLinkResolver() {
  const { taskId } = useParams<{ taskId: string }>();
  const { isAuthenticated } = useAuthStore();
  const location = useLocation();
  const { workspaces, fetchWorkspaces } = useWorkspaceStore();
  const [target, setTarget] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isAuthenticated || !taskId) return;
    let cancelled = false;
    (async () => {
      try {
        const task = await api<Task>(`/api/v1/tasks/${taskId}`);
        if (cancelled) return;
        if (workspaces.length === 0) await fetchWorkspaces();
        const allWorkspaces = useWorkspaceStore.getState().workspaces;
        let projectSlug: string | null = null;
        let wsSlug: string | null = null;
        for (const ws of allWorkspaces) {
          const page = await api<PaginatedResponse<Project>>(
            `/api/v1/workspaces/${ws.id}/projects`,
          );
          const found = (page.items ?? []).find((p) => p.id === task.project_id);
          if (found) {
            projectSlug = found.slug;
            wsSlug = ws.slug;
            break;
          }
        }
        if (cancelled) return;
        if (!projectSlug || !wsSlug) {
          setError("Task is in a workspace or project you don't have access to.");
          return;
        }
        setTarget(`/w/${wsSlug}/p/${projectSlug}/t/${task.id}`);
      } catch {
        setError("Task not found or you don't have access.");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [taskId, isAuthenticated, fetchWorkspaces, workspaces.length]);

  if (!isAuthenticated) {
    return (
      <Navigate
        to={`/login?redirect=${encodeURIComponent(location.pathname)}`}
        replace
      />
    );
  }
  if (error) {
    return (
      <div className="mx-auto max-w-md p-8 text-center">
        <h1 className="text-lg font-semibold">Task not found</h1>
        <p className="mt-2 text-sm text-muted-foreground">{error}</p>
      </div>
    );
  }
  if (!target) {
    return (
      <div className="mx-auto max-w-6xl space-y-6 px-4 py-6">
        <Skeleton className="h-8 w-32" />
        <Skeleton className="h-10 w-2/3" />
        <Skeleton className="h-4 w-full" />
      </div>
    );
  }
  return <Navigate to={target} replace />;
}
