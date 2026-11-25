// The project's «Statistics» tab (/w/:ws/p/:project/stats): the agent statistics widget pinned to this project.
// The layout binds the current project from the slug; until that project is the one in the URL nothing renders,
// so a stale project from the previous visit is never pinned and the widget never reads unfiltered. Once the
// project list has answered without this slug (missing project, or a workspace member outside the project) the
// tab says so the way the board does, instead of waiting on a project that will never be bound.
import { useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ProjectNotFound } from "@/components/project-not-found";
import { PinnedAgentUsage } from "@/core/agent-usage/pinned";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";

export function ProjectStatsPage() {
  const { t } = useTranslation("settings");
  const { wsSlug, projectSlug } = useParams<{ wsSlug: string; projectSlug: string }>();
  const project = useProjectStore((s) => s.currentProject);
  const ready = project !== null && project.slug === projectSlug;
  const urlWorkspaceId = useWorkspaceStore((s) => s.workspaces.find((w) => w.slug === wsSlug)?.id);
  // Final only for the URL's own workspace: right after a workspace jump the store still holds the previous list.
  const notFound = useProjectStore(
    (s) =>
      !!urlWorkspaceId &&
      s.loadedWorkspaceId === urlWorkspaceId &&
      !s.isLoading &&
      !s.projects.some((p) => p.slug === projectSlug),
  );

  if (!ready && notFound) return <ProjectNotFound slug={projectSlug} />;

  return (
    <div className="mx-auto flex w-full max-w-[1600px] min-w-0 flex-col gap-4 pb-10" data-testid="project-stats-page">
      <header className="min-w-0">
        <h1 className="truncate text-lg font-semibold md:text-2xl" title={ready ? project.name : undefined}>
          {ready ? project.name : <Skeleton className="h-7 w-48" />}
        </h1>
        <p className="text-sm text-muted-foreground">{t("viewTabs.stats.subtitle")}</p>
      </header>
      <Card className="min-w-0">
        <CardContent className="min-w-0 pt-4">
          {ready ? (
            <PinnedAgentUsage key={project.id} pin={{ kind: "project", id: project.id }} title={t("viewTabs.stats.title")} />
          ) : (
            <div className="space-y-2" aria-busy="true">
              <Skeleton className="h-6 w-40" />
              <Skeleton className="h-32 w-full" />
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
