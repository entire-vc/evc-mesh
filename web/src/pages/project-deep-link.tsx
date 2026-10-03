import { useEffect, useState } from "react";
import { Navigate, useLocation, useParams } from "react-router";
import { useAuthStore } from "@/stores/auth";
import { api } from "@/lib/api";
import { Skeleton } from "@/components/ui/skeleton";
import type { Project, Workspace } from "@/types";

// Project UUID links remain stable when either slug is renamed. The project
// already carries workspace_id, so no workspace/project enumeration is needed.
export function ProjectDeepLinkResolver() {
  const { projectId } = useParams<{ projectId: string }>();
  const { isAuthenticated } = useAuthStore();
  const location = useLocation();
  const requestKey = location.pathname + location.search + location.hash;
  const [result, setResult] = useState<{
    key: string;
    target?: string;
    error?: string;
  } | null>(null);

  useEffect(() => {
    if (!isAuthenticated || !projectId) return;
    let cancelled = false;
    setResult(null);
    (async () => {
      try {
        const project = await api<Project>(`/api/v1/projects/${projectId}`);
        if (cancelled) return;
        const workspace = await api<Workspace>(
          `/api/v1/workspaces/${project.workspace_id}`,
        );
        if (cancelled) return;
        setResult({
          key: requestKey,
          target: `/w/${workspace.slug}/p/${project.slug}${location.search}${location.hash}`,
        });
      } catch {
        if (!cancelled) {
          setResult({
            key: requestKey,
            error: "Project not found or you don't have access.",
          });
        }
      }
    })();
    return () => { cancelled = true; };
  }, [projectId, isAuthenticated, requestKey, location.search, location.hash]);

  if (!isAuthenticated) {
    return (
      <Navigate to={`/login?redirect=${encodeURIComponent(requestKey)}`} replace />
    );
  }
  const current = result?.key === requestKey ? result : null;
  if (current?.error) {
    return (
      <div className="mx-auto max-w-md p-8 text-center">
        <h1 className="text-lg font-semibold">Project not found</h1>
        <p className="mt-2 text-sm text-muted-foreground">{current.error}</p>
      </div>
    );
  }
  if (current?.target) return <Navigate to={current.target} replace />;
  return (
    <div className="mx-auto max-w-6xl space-y-6 px-4 py-6" role="status" aria-label="Loading project">
      <Skeleton className="h-8 w-32" />
      <Skeleton className="h-10 w-2/3" />
      <Skeleton className="h-4 w-full" />
    </div>
  );
}
