import { useEffect, useState } from "react";
import { Navigate, useLocation, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { useAuthStore } from "@/stores/auth";
import { api } from "@/lib/api";
import { Skeleton } from "@/components/ui/skeleton";
import type { Project, Workspace } from "@/types";

// Project UUID links remain stable when either slug is renamed. The project
// already carries workspace_id, so no workspace/project enumeration is needed.
export function ProjectDeepLinkResolver() {
  const { t } = useTranslation("misc");
  const { projectId } = useParams<{ projectId: string }>();
  const { isAuthenticated } = useAuthStore();
  const location = useLocation();
  const requestKey = location.pathname + location.search + location.hash;
  const [result, setResult] = useState<{
    key: string;
    target?: string;
    error?: boolean;
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
            error: true,
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
        <h1 className="text-lg font-semibold">{t("deepLink.projectNotFound")}</h1>
        <p className="mt-2 text-sm text-muted-foreground">{t("deepLink.projectNoAccess")}</p>
      </div>
    );
  }
  if (current?.target) return <Navigate to={current.target} replace />;
  return (
    <div className="mx-auto max-w-6xl space-y-6 px-4 py-6" role="status" aria-label={t("deepLink.loadingProject")}>
      <Skeleton className="h-8 w-32" />
      <Skeleton className="h-10 w-2/3" />
      <Skeleton className="h-4 w-full" />
    </div>
  );
}
