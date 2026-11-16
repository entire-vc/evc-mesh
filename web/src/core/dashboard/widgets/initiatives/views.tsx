// Views of the `initiatives` and `initiative` widgets (PRD dashboards §5.8).
// Each fits its grid cell through container queries, never the window; both
// lead to the initiative page w/:ws/initiatives/:id.
import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { Calendar, ExternalLink, Plus, Target } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Initiative, InitiativeStatus } from "@/types";
import { initiativeHref, selectInitiatives, useInitiative, useInitiatives, type InitiativeSort } from "./hooks";
import { formatTargetDate, InitiativeCard, InitiativeForm, InitiativeProgress, LinkedProjects, StatusBadge } from "./parts";
import { useInitiativeRights } from "./rights";

export interface InitiativesWidgetSettings {
  /** Statuses to show; empty or absent shows all. */
  statuses?: InitiativeStatus[];
  sort?: InitiativeSort;
  view?: "grid" | "list";
}

export interface InitiativeWidgetSettings {
  initiativeId?: string;
}

function WidgetSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2" aria-busy="true">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-14 w-full" />
      ))}
    </div>
  );
}

function WidgetError({ retry }: { retry: () => void }) {
  const { t } = useTranslation("settings");
  return (
    <div role="alert" className="flex flex-col items-center gap-2 py-6 text-center text-sm text-muted-foreground">
      <p>{t("initiatives.widget.loadFailed")}</p>
      <Button size="sm" variant="outline" onClick={retry}>
        {t("common:retry", { defaultValue: "Retry" })}
      </Button>
    </div>
  );
}

function WidgetEmpty({ text, children }: { text: string; children?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 py-6 text-center">
      <Target className="h-8 w-8 text-muted-foreground" />
      <p className="text-sm text-muted-foreground">{text}</p>
      {children}
    </div>
  );
}

/** Make sure the workspace's projects are in the store for the create/link pickers. */
function useWorkspaceProjects() {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const { projects, fetchProjects } = useProjectStore();
  useEffect(() => {
    if (wsId && projects.length === 0) void fetchProjects(wsId);
  }, [wsId, projects.length, fetchProjects]);
  return { wsId, projects };
}

/** `initiatives`: the workspace's initiatives, filtered and sorted by the instance settings. */
export function InitiativesWidget({ settings = {} }: { settings?: InitiativesWidgetSettings }) {
  const { t } = useTranslation("settings");
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const navigate = useNavigate();
  const rights = useInitiativeRights();
  const { wsId, projects } = useWorkspaceProjects();
  const data = useInitiatives();
  const [creating, setCreating] = useState(false);
  const view = settings.view ?? "grid";

  const onCreated = useCallback(
    (saved: Initiative) => {
      setCreating(false);
      navigate(initiativeHref(wsSlug, saved.id));
    },
    [navigate, wsSlug],
  );

  const createButton = rights.create && !creating && (
    <Button size="sm" onClick={() => setCreating(true)}>
      <Plus className="h-4 w-4" />
      {t("initiatives.page.newInitiative")}
    </Button>
  );

  let body: React.ReactNode;
  if (data.state === "loading") body = <WidgetSkeleton />;
  else if (data.state === "error") body = <WidgetError retry={data.retry} />;
  else {
    const items = selectInitiatives(data.value, { statuses: settings.statuses, sort: settings.sort });
    if (data.value.length === 0) {
      body = <WidgetEmpty text={t("initiatives.page.emptyText")} />;
    } else if (items.length === 0) {
      body = <WidgetEmpty text={t("initiatives.widget.noneWithStatus")} />;
    } else if (view === "list") {
      body = (
        <div className="space-y-1.5">
          {items.map((ini) => (
            <InitiativeCard key={ini.id} initiative={ini} href={initiativeHref(wsSlug, ini.id)} layout="list" />
          ))}
        </div>
      );
    } else {
      body = (
        <div className="grid grid-cols-1 gap-3 @lg:grid-cols-2 @4xl:grid-cols-3">
          {items.map((ini) => (
            <InitiativeCard key={ini.id} initiative={ini} href={initiativeHref(wsSlug, ini.id)} />
          ))}
        </div>
      );
    }
  }

  return (
    <div className="@container flex h-full min-w-0 flex-col gap-3" data-widget="initiatives">
      {createButton && <div className="flex justify-end">{createButton}</div>}
      {creating && wsId && (
        <InitiativeForm
          workspaceId={wsId}
          projects={projects}
          onDone={onCreated}
          onCancel={() => setCreating(false)}
        />
      )}
      <div className="min-h-0 flex-1 overflow-auto">{body}</div>
    </div>
  );
}

/** `initiative`: one chosen initiative with its progress, deadline and linked projects. */
export function InitiativeWidget({ settings = {} }: { settings?: InitiativeWidgetSettings }) {
  const { t } = useTranslation("settings");
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const rights = useInitiativeRights();
  const { projects } = useWorkspaceProjects();
  const data = useInitiative(settings.initiativeId);

  if (!settings.initiativeId) return <WidgetEmpty text={t("initiatives.widget.pickOne")} />;
  if (data.state === "loading") return <WidgetSkeleton rows={2} />;
  if (data.state === "error") return <WidgetError retry={data.retry} />;

  const ini = data.value;
  const targetDate = formatTargetDate(ini.target_date);
  return (
    <div className="@container flex h-full min-w-0 flex-col gap-3 overflow-auto" data-widget="initiative">
      <div className="flex flex-wrap items-center gap-2">
        <Target className="h-4 w-4 shrink-0 text-muted-foreground" />
        <Link
          to={initiativeHref(wsSlug, ini.id)}
          className="min-w-0 flex-1 truncate font-semibold hover:underline"
        >
          {ini.name}
        </Link>
        <StatusBadge status={ini.status} />
        <Link
          to={initiativeHref(wsSlug, ini.id)}
          className="text-muted-foreground hover:text-foreground"
          aria-label={t("initiatives.widget.openPage")}
          title={t("initiatives.widget.openPage")}
        >
          <ExternalLink className="h-3.5 w-3.5" />
        </Link>
      </div>
      <InitiativeProgress initiative={ini} />
      {targetDate && (
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <Calendar className="h-3 w-3" />
          {t("initiatives.card.target", { date: targetDate })}
        </div>
      )}
      <LinkedProjects initiative={ini} projects={projects} rights={rights} onChanged={data.reload} compact />
    </div>
  );
}

export const views = { initiatives: InitiativesWidget, initiative: InitiativeWidget };
