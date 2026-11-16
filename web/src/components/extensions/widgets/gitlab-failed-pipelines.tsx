import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { ExternalLink, XCircle } from "lucide-react";
import { api } from "@/lib/api";
import { formatDateTime } from "@/i18n/format";
import { useWorkspaceStore } from "@/stores/workspace";
import type { ExtensionWidgetProps } from "./builtin-widgets";

export interface FailedPipeline {
  link_id: string;
  url: string;
  title: string;
  external_id: string;
  pipeline_updated_at: string;
  task_id: string;
  task_title: string;
  project_id: string;
  project_name: string;
}

type Load = { state: "loading" } | { state: "error" } | { state: "ok"; items: FailedPipeline[] };

/**
 * GitLab extension widget "Failed pipelines of my MRs": open merge requests of
 * the viewer's tasks whose latest pipeline failed in the last 24 h, from the
 * VCS links the tasks already carry. `settings.project_id` narrows to one
 * project bound to GitLab; without it every such project the viewer may open.
 */
export default function GitlabFailedPipelinesWidget({ settings }: ExtensionWidgetProps) {
  const { t } = useTranslation("integrations");
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const projectId = typeof settings.project_id === "string" ? settings.project_id : "";
  const [load, setLoad] = useState<Load>({ state: "loading" });

  useEffect(() => {
    if (!wsId) return;
    let alive = true;
    setLoad({ state: "loading" });
    const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
    api<{ items: FailedPipeline[] | null }>(`/api/v1/workspaces/${wsId}/widgets/gitlab/failed-pipelines${q}`)
      .then((res) => alive && setLoad({ state: "ok", items: res.items ?? [] }))
      .catch(() => alive && setLoad({ state: "error" }));
    return () => {
      alive = false;
    };
  }, [wsId, projectId]);

  if (load.state === "loading") {
    return (
      <div className="space-y-2 p-3" aria-busy="true" data-testid="gitlab-failed-pipelines-loading">
        <div className="h-4 w-40 animate-pulse rounded bg-muted" />
        <div className="h-4 w-full animate-pulse rounded bg-muted" />
      </div>
    );
  }
  if (load.state === "error") {
    return <p className="p-3 text-sm text-muted-foreground" role="status">{t("widgets.gitlabFailedPipelines.error")}</p>;
  }
  if (load.items.length === 0) {
    return (
      <p className="p-3 text-sm text-muted-foreground" role="status" data-testid="gitlab-failed-pipelines-empty">
        {t("widgets.gitlabFailedPipelines.empty")}
      </p>
    );
  }
  return (
    <ul className="divide-y divide-border overflow-y-auto" data-testid="gitlab-failed-pipelines">
      {load.items.map((p) => (
        <li key={p.link_id} className="flex items-start gap-2 px-3 py-2 text-sm">
          <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" aria-label={t("widgets.gitlabFailedPipelines.failed")} />
          <div className="min-w-0 flex-1">
            <Link to={`/t/${p.task_id}`} className="block truncate font-medium hover:underline">
              {p.task_title}
            </Link>
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <a href={p.url} target="_blank" rel="noopener noreferrer" className="inline-flex min-w-0 items-center gap-1 hover:underline">
                <span className="truncate">!{p.external_id} {p.title}</span>
                <ExternalLink className="h-3 w-3 shrink-0" aria-hidden />
              </a>
              <span className="shrink-0">· {p.project_name}</span>
              <time className="ml-auto shrink-0" dateTime={p.pipeline_updated_at}>
                {formatDateTime(p.pipeline_updated_at)}
              </time>
            </div>
          </div>
        </li>
      ))}
    </ul>
  );
}
