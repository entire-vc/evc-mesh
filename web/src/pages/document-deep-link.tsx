import { useEffect, useState } from "react";
import { Navigate, useParams, useLocation } from "react-router";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { api } from "@/lib/api";
import { Skeleton } from "@/components/ui/skeleton";
import type { PaginatedResponse, Project, ProjectDocument } from "@/types";

// Resolves /d/:docId — the flat link a Document.url now carries — into the
// full /w/:wsSlug/p/:projectSlug/docs/:docId route. Same shape as
// TaskDeepLinkResolver (task-deep-link.tsx): the document only knows its
// project_id, not the project's or workspace's slug, so this looks them up
// client-side rather than making the handler join through them (task #14db79fd).
export function DocumentDeepLinkResolver() {
  const { docId } = useParams<{ docId: string }>();
  const { isAuthenticated } = useAuthStore();
  const location = useLocation();
  const { workspaces, fetchWorkspaces } = useWorkspaceStore();
  const [target, setTarget] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isAuthenticated || !docId) return;
    let cancelled = false;
    (async () => {
      try {
        const doc = await api<ProjectDocument>(`/api/v1/documents/${docId}`);
        if (cancelled) return;
        if (workspaces.length === 0) await fetchWorkspaces();
        const allWorkspaces = useWorkspaceStore.getState().workspaces;
        let projectSlug: string | null = null;
        let wsSlug: string | null = null;
        for (const ws of allWorkspaces) {
          const page = await api<PaginatedResponse<Project>>(
            `/api/v1/workspaces/${ws.id}/projects`,
          );
          const found = (page.items ?? []).find(
            (p) => p.id === doc.project_id,
          );
          if (found) {
            projectSlug = found.slug;
            wsSlug = ws.slug;
            break;
          }
        }
        if (cancelled) return;
        if (!projectSlug || !wsSlug) {
          setError("Document is in a workspace or project you don't have access to.");
          return;
        }
        // Preserve the query string / hash (a paragraph anchor, §D6) across the
        // redirect — this resolver only adds the slugs, it doesn't drop what
        // the caller already had.
        setTarget(
          `/w/${wsSlug}/p/${projectSlug}/docs/${doc.id}${location.search}${location.hash}`,
        );
      } catch {
        setError("Document not found or you don't have access.");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [docId, isAuthenticated, fetchWorkspaces, workspaces.length]);

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
        <h1 className="text-lg font-semibold">Document not found</h1>
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
