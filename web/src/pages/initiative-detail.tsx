// w/:ws/initiatives/:id — the initiative's own page (PRD dashboards
// §5.8). It used to replace the grid in place without changing the address;
// now both initiatives widgets and the old list link here, and a direct link
// opens it. Edit goes through the existing PATCH, delete needs delete_project.
import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Calendar, Pencil, Target, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import { useInitiativeStore } from "@/stores/initiative";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useInitiative } from "@/core/dashboard/widgets/initiatives/hooks";
import {
  formatTargetDate,
  InitiativeForm,
  InitiativeProgress,
  LinkedProjects,
  StatusBadge,
} from "@/core/dashboard/widgets/initiatives/parts";
import { useInitiativeRights } from "@/core/dashboard/widgets/initiatives/rights";

export function InitiativeDetailPage() {
  const { t } = useTranslation("settings");
  // The id is the splat of w/:ws/initiatives/* (App.tsx): one route for the list and the page.
  const { wsSlug, "*": initiativeId } = useParams();
  const navigate = useNavigate();
  const rights = useInitiativeRights();
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const { projects, fetchProjects } = useProjectStore();
  const { deleteInitiative } = useInitiativeStore();
  const data = useInitiative(initiativeId);
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  // The list is a dashboard widget now (§5.9): back and «after delete» go to the home dashboard.
  const listHref = wsSlug ? `/w/${wsSlug}/dashboard` : "/";

  useEffect(() => {
    if (wsId && projects.length === 0) void fetchProjects(wsId);
  }, [wsId, projects.length, fetchProjects]);

  const ini = data.state === "ready" ? data.value : null;

  const handleDelete = useCallback(async () => {
    if (!ini) return;
    if (!confirm(t("initiatives.confirmDelete", { name: ini.name }))) return;
    setDeleting(true);
    try {
      await deleteInitiative(ini.id);
      toast.success(t("initiatives.toast.deleted"));
      navigate(listHref);
    } catch (err) {
      toast.error(apiErrorMessage(err, t("initiatives.toast.deleteFailed")));
      setDeleting(false);
    }
  }, [ini, deleteInitiative, navigate, listHref, t]);

  const back = (
    <Link
      to={listHref}
      className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 py-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeft className="h-4 w-4" />
      {t("initiatives.detail.backToDashboard")}
    </Link>
  );

  if (data.state === "loading") {
    return (
      <div className="space-y-4" aria-busy="true">
        {back}
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-4 w-96 max-w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (data.state === "error") {
    return (
      <div className="space-y-4">
        {back}
        <Card>
          <CardContent role="alert" className="flex flex-col items-center gap-3 py-12 text-center">
            <Target className="h-10 w-10 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">{t("initiatives.detail.notFound")}</p>
            <Button size="sm" variant="outline" onClick={data.retry}>
              {t("common:retry", { defaultValue: "Retry" })}
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  const initiative = data.value;
  const targetDate = formatTargetDate(initiative.target_date);

  return (
    <div className="mx-auto max-w-4xl space-y-6" data-page="initiative">
      {back}
      {editing && wsId ? (
        <InitiativeForm
          workspaceId={wsId}
          projects={projects}
          initiative={initiative}
          onDone={() => {
            setEditing(false);
            data.reload();
          }}
          onCancel={() => setEditing(false)}
        />
      ) : (
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex items-center gap-3">
              <Target className="h-6 w-6 shrink-0 text-muted-foreground" />
              <h1 className="min-w-0 break-words text-2xl font-bold">{initiative.name}</h1>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <StatusBadge status={initiative.status} />
              {targetDate && (
                <span className="flex items-center gap-1 text-xs text-muted-foreground">
                  <Calendar className="h-3 w-3" />
                  {t("initiatives.card.target", { date: targetDate })}
                </span>
              )}
            </div>
            {initiative.description && (
              <p className="whitespace-pre-line text-sm text-muted-foreground">{initiative.description}</p>
            )}
          </div>
          {(rights.edit || rights.delete) && (
            <div className="flex shrink-0 gap-1">
              {rights.edit && (
                <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
                  <Pencil className="h-4 w-4" />
                  {t("initiatives.detail.edit")}
                </Button>
              )}
              {rights.delete && (
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => void handleDelete()}
                  disabled={deleting}
                  aria-label={t("initiatives.detail.delete")}
                  title={t("initiatives.detail.delete")}
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              )}
            </div>
          )}
        </header>
      )}

      <Card>
        <CardContent className="pt-6">
          <InitiativeProgress initiative={initiative} />
        </CardContent>
      </Card>

      <LinkedProjects initiative={initiative} projects={projects} rights={rights} onChanged={data.reload} />
    </div>
  );
}
