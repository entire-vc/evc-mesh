import * as i18nFmt from "@/i18n/format";
import { useTranslation } from "react-i18next";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import {
  Calendar,
  FolderKanban,
  Link2,
  Link2Off,
  Plus,
  Target,
  Trash2,
} from "lucide-react";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useInitiativeStore } from "@/stores/initiative";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select } from "@/components/ui/select";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { apiErrorMessage } from "@/lib/api-error";
import type {
  CreateInitiativeRequest,
  Initiative,
  InitiativeStatus,
  Project,
} from "@/types";

const STATUS_COLORS: Record<InitiativeStatus, string> = {
  active:
    "bg-tone-positive-100 text-tone-positive-800 dark:bg-tone-positive-900 dark:text-tone-positive-200",
  completed:
    "bg-tone-info-100 text-tone-info-800 dark:bg-tone-info-900 dark:text-tone-info-200",
  archived:
    "bg-tone-neutral-100 text-tone-neutral-600 dark:bg-tone-neutral-800 dark:text-tone-neutral-400",
};

function InitiativeProgress({ initiative }: { initiative: Initiative }) {
  const { t } = useTranslation("settings");
  const total = initiative.total_tasks ?? 0;
  const completed = initiative.completed_tasks ?? 0;
  const pct = total > 0 ? Math.round((completed / total) * 100) : 0;

  if (initiative.status === "completed") {
    return (
      <div className="space-y-1">
        <div className="flex items-center gap-2">
          <div className="h-2 flex-1 rounded-full bg-muted">
            <div className="h-2 rounded-full bg-primary transition-all" style={{ width: "100%" }} />
          </div>
          <span className="shrink-0 text-xs tabular-nums text-muted-foreground">100%</span>
        </div>
        <p className="text-xs text-muted-foreground">{t("initiatives.progress.completed")}</p>
      </div>
    );
  }

  if (total === 0) {
    return <span className="text-xs text-muted-foreground">{t("initiatives.progress.noTasks")}</span>;
  }

  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2">
        <div className="h-2 flex-1 rounded-full bg-muted">
          <div className="h-2 rounded-full bg-primary transition-all" style={{ width: `${pct}%` }} />
        </div>
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{pct}%</span>
      </div>
      <p className="text-xs text-muted-foreground">{t("initiatives.progress.tasksDone", { completed, total })}</p>
    </div>
  );
}

function InitiativeCard({
  initiative,
  onSelect,
}: {
  initiative: Initiative;
  onSelect: (ini: Initiative) => void;
}) {
  const { t } = useTranslation("settings");
  const statusCfg = STATUS_COLORS[initiative.status] ?? STATUS_COLORS.active;
  const targetDate = initiative.target_date
    ? i18nFmt.formatDate(initiative.target_date, { month: "short", day: "numeric", year: "numeric" })
    : null;

  return (
    <Card
      className="cursor-pointer transition-shadow hover:shadow-md"
      onClick={() => onSelect(initiative)}
    >
      <CardHeader className="pb-2">
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2 flex-1 min-w-0">
            <Target className="h-4 w-4 shrink-0 text-muted-foreground" />
            <CardTitle className="truncate text-base">{initiative.name}</CardTitle>
          </div>
          <span className={cn("shrink-0 rounded-full px-2 py-0.5 text-xs font-medium", statusCfg)}>
            {t(`initiatives.statusBadge.${initiative.status}`, { defaultValue: initiative.status })}
          </span>
        </div>
        {initiative.description && (
          <p className="text-sm text-muted-foreground line-clamp-2">{initiative.description}</p>
        )}
      </CardHeader>
      <CardContent className="space-y-2">
        <InitiativeProgress initiative={initiative} />
        {targetDate && (
          <div className="flex items-center gap-1 text-xs text-muted-foreground">
            <Calendar className="h-3 w-3" />
            {t("initiatives.card.target", { date: targetDate })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function NewInitiativeForm({
  workspaceId,
  projects,
  onClose,
}: {
  workspaceId: string;
  projects: Project[];
  onClose: () => void;
}) {
  const { t } = useTranslation("settings");
  const { createInitiative, linkProject } = useInitiativeStore();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [status, setStatus] = useState<InitiativeStatus>("active");
  const [targetDate, setTargetDate] = useState<string | null>(null);
  const [projectId, setProjectId] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      if (!name.trim()) return;
      setSubmitting(true);
      try {
        const req: CreateInitiativeRequest = {
          name: name.trim(),
          description: description.trim(),
          status,
          target_date: targetDate || null,
        };
        const initiative = await createInitiative(workspaceId, req);
        if (projectId) {
          await linkProject(initiative.id, projectId);
        }
        toast.success(t("initiatives.toast.created"));
        onClose();
      } catch (err) {
        toast.error(apiErrorMessage(err, t("initiatives.toast.createFailed")));
      } finally {
        setSubmitting(false);
      }
    },
    [name, description, status, targetDate, projectId, workspaceId, createInitiative, linkProject, onClose, t],
  );

  return (
    <Card className="border-primary/30">
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{t("initiatives.form.heading")}</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1">
            <label className="text-sm font-medium">{t("initiatives.form.name")}</label>
            <Input
              placeholder={t("initiatives.form.namePlaceholder")}
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              autoFocus
            />
          </div>
          <div className="space-y-1">
            <label className="text-sm font-medium">{t("initiatives.form.description")}</label>
            <Textarea
              placeholder={t("initiatives.form.descriptionPlaceholder")}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
            />
          </div>
          <div className="space-y-1">
            <label className="text-sm font-medium">{t("initiatives.form.project")}</label>
            <Select
              value={projectId}
              onChange={(e) => setProjectId(e.target.value)}
            >
              <option value="">{t("initiatives.form.noProject")}</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>{p.name}</option>
              ))}
            </Select>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1">
              <label className="text-sm font-medium">{t("initiatives.form.status")}</label>
              <Select
                value={status}
                onChange={(e) => setStatus(e.target.value as InitiativeStatus)}
              >
                <option value="active">{t("initiatives.status.active")}</option>
                <option value="completed">{t("initiatives.status.completed")}</option>
                <option value="archived">{t("initiatives.status.archived")}</option>
              </Select>
            </div>
            <div className="space-y-1">
              <label className="text-sm font-medium">{t("initiatives.form.targetDate")}</label>
              <Input
                type="date"
                value={targetDate ?? ""}
                onChange={(e) => setTargetDate(e.target.value || null)}
              />
            </div>
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              {t("initiatives.form.cancel")}
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting ? t("initiatives.form.creating") : t("initiatives.form.create")}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function InitiativeDetail({
  initiative,
  onBack,
}: {
  initiative: Initiative;
  onBack: () => void;
}) {
  const { t } = useTranslation("settings");
  const { getInitiative, linkProject, unlinkProject, deleteInitiative } = useInitiativeStore();
  const { projects } = useProjectStore();
  const [detail, setDetail] = useState<Initiative>(initiative);
  const [selectedProjectId, setSelectedProjectId] = useState("");
  const [linking, setLinking] = useState(false);
  const [deleting, setDeleting] = useState(false);

  useEffect(() => {
    // Always fetch fresh data with linked projects.
    getInitiative(initiative.id).then(setDetail).catch(() => {});
  }, [initiative.id, getInitiative]);

  const linkedIds = new Set((detail.linked_projects ?? []).map((p) => p.id));
  const availableProjects = projects.filter((p) => !linkedIds.has(p.id));

  const handleLink = useCallback(async () => {
    if (!selectedProjectId) return;
    setLinking(true);
    try {
      await linkProject(detail.id, selectedProjectId);
      const updated = await getInitiative(detail.id);
      setDetail(updated);
      setSelectedProjectId("");
      toast.success(t("initiatives.toast.linked"));
    } catch (err) {
      toast.error(apiErrorMessage(err, t("initiatives.toast.linkFailed")));
    } finally {
      setLinking(false);
    }
  }, [selectedProjectId, detail.id, linkProject, getInitiative, t]);

  const handleUnlink = useCallback(
    async (projectId: string) => {
      try {
        await unlinkProject(detail.id, projectId);
        const updated = await getInitiative(detail.id);
        setDetail(updated);
        toast.success(t("initiatives.toast.unlinked"));
      } catch (err) {
        toast.error(apiErrorMessage(err, t("initiatives.toast.unlinkFailed")));
      }
    },
    [detail.id, unlinkProject, getInitiative, t],
  );

  const handleDelete = useCallback(async () => {
    if (!confirm(t("initiatives.confirmDelete", { name: detail.name }))) return;
    setDeleting(true);
    try {
      await deleteInitiative(detail.id);
      toast.success(t("initiatives.toast.deleted"));
      onBack();
    } catch (err) {
      toast.error(apiErrorMessage(err, t("initiatives.toast.deleteFailed")));
      setDeleting(false);
    }
  }, [detail, deleteInitiative, onBack, t]);

  const statusCfg = STATUS_COLORS[detail.status] ?? STATUS_COLORS.active;
  const targetDate = detail.target_date
    ? i18nFmt.formatDate(detail.target_date, { month: "short", day: "numeric", year: "numeric" })
    : null;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <Button variant="ghost" size="sm" onClick={onBack} className="mb-2 -ml-2">
            {t("initiatives.detail.back")}
          </Button>
          <div className="flex items-center gap-3">
            <Target className="h-6 w-6 text-muted-foreground" />
            <div>
              <h1 className="text-2xl font-bold">{detail.name}</h1>
              <div className="flex items-center gap-2 mt-1">
                <span className={cn("rounded-full px-2 py-0.5 text-xs font-medium", statusCfg)}>
                  {t(`initiatives.statusBadge.${detail.status}`, { defaultValue: detail.status })}
                </span>
                {targetDate && (
                  <span className="flex items-center gap-1 text-xs text-muted-foreground">
                    <Calendar className="h-3 w-3" />
                    {targetDate}
                  </span>
                )}
              </div>
            </div>
          </div>
          {detail.description && (
            <p className="mt-2 text-sm text-muted-foreground">{detail.description}</p>
          )}
        </div>
        <Button variant="ghost" size="icon" onClick={handleDelete} disabled={deleting}>
          <Trash2 className="h-4 w-4 text-destructive" />
        </Button>
      </div>

      {/* Link new project */}
      {availableProjects.length > 0 && (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-sm">{t("initiatives.detail.linkProject")}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex gap-2">
              <Select
                value={selectedProjectId}
                onChange={(e) => setSelectedProjectId(e.target.value)}
                className="flex-1"
              >
                <option value="">{t("initiatives.detail.selectProject")}</option>
                {availableProjects.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </Select>
              <Button onClick={handleLink} disabled={!selectedProjectId || linking}>
                <Link2 className="h-4 w-4" />
                {t("initiatives.detail.link")}
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Linked projects */}
      <div>
        <h2 className="text-lg font-semibold mb-3">
          {t("initiatives.detail.linkedProjects", { n: detail.linked_projects?.length ?? 0 })}
        </h2>
        {(detail.linked_projects ?? []).length === 0 ? (
          <Card>
            <CardContent className="py-8 text-center">
              <FolderKanban className="mx-auto mb-3 h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">{t("initiatives.detail.noLinked")}</p>
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-2">
            {(detail.linked_projects ?? []).map((project: Project) => (
              <Card key={project.id}>
                <CardContent className="flex items-center gap-3 py-3">
                  <FolderKanban className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium truncate">{project.name}</p>
                    {project.description && (
                      <p className="text-xs text-muted-foreground truncate">{project.description}</p>
                    )}
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 shrink-0"
                    onClick={() => handleUnlink(project.id)}
                  >
                    <Link2Off className="h-3.5 w-3.5" />
                  </Button>
                </CardContent>
              </Card>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export function InitiativesPage() {
  const { t } = useTranslation("settings");
  const { currentWorkspace } = useWorkspaceStore();
  const { projects, fetchProjects } = useProjectStore();
  const { initiatives, isLoading, fetchInitiatives } = useInitiativeStore();
  const [showForm, setShowForm] = useState(false);
  const [selectedInitiative, setSelectedInitiative] = useState<Initiative | null>(null);

  useEffect(() => {
    if (currentWorkspace) {
      fetchInitiatives(currentWorkspace.id);
      if (projects.length === 0) {
        fetchProjects(currentWorkspace.id);
      }
    }
  }, [currentWorkspace, fetchInitiatives, fetchProjects, projects.length]);

  if (selectedInitiative) {
    return (
      <InitiativeDetail
        initiative={selectedInitiative}
        onBack={() => setSelectedInitiative(null)}
      />
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex justify-end">
        <Button onClick={() => setShowForm(true)}>
          <Plus className="h-4 w-4" />
          {t("initiatives.page.newInitiative")}
        </Button>
      </div>

      {showForm && currentWorkspace && (
        <NewInitiativeForm
          workspaceId={currentWorkspace.id}
          projects={projects}
          onClose={() => setShowForm(false)}
        />
      )}

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-5 w-40" />
                <Skeleton className="h-4 w-56 mt-1" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-2 w-full" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : initiatives.length === 0 && !showForm ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <Target className="mb-4 h-12 w-12 text-muted-foreground" />
            <h3 className="mb-2 text-lg font-semibold">{t("initiatives.page.emptyTitle")}</h3>
            <p className="mb-4 text-sm text-muted-foreground">
              {t("initiatives.page.emptyText")}
            </p>
            <Button onClick={() => setShowForm(true)}>
              <Plus className="h-4 w-4" />
              {t("initiatives.page.createInitiative")}
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {initiatives.map((initiative) => (
            <InitiativeCard
              key={initiative.id}
              initiative={initiative}
              onSelect={setSelectedInitiative}
            />
          ))}
        </div>
      )}
    </div>
  );
}
