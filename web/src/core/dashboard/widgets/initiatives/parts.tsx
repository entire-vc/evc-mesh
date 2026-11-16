// Building blocks shared by the initiatives widgets and the initiative page:
// one markup for a card, a progress bar, the create/edit form and the linked
// projects list, so the widget and the page never drift apart. Every action is
// drawn only when the viewer's role allows it (rights.ts).
import * as i18nFmt from "@/i18n/format";
import { useTranslation } from "react-i18next";
import { type FormEvent, useCallback, useState } from "react";
import { Link } from "react-router";
import { Calendar, FolderKanban, Link2, Link2Off, Target } from "lucide-react";
import { useInitiativeStore } from "@/stores/initiative";
import { Button } from "@/components/ui/button";
import { EntityIcon } from "@/components/ui/entity-icon";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select } from "@/components/ui/select";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { apiErrorMessage } from "@/lib/api-error";
import type { CreateInitiativeRequest, Initiative, InitiativeStatus, Project } from "@/types";
import { progressPct } from "./hooks";
import type { InitiativeRights } from "./rights";

export const INITIATIVE_STATUSES: readonly InitiativeStatus[] = ["active", "completed", "archived"];

const STATUS_COLORS: Record<InitiativeStatus, string> = {
  active:
    "bg-tone-positive-100 text-tone-positive-800 dark:bg-tone-positive-900 dark:text-tone-positive-200",
  completed:
    "bg-tone-info-100 text-tone-info-800 dark:bg-tone-info-900 dark:text-tone-info-200",
  archived:
    "bg-tone-neutral-100 text-tone-neutral-600 dark:bg-tone-neutral-800 dark:text-tone-neutral-400",
};

export function formatTargetDate(date: string | null): string | null {
  return date ? i18nFmt.formatDate(date, { month: "short", day: "numeric", year: "numeric" }) : null;
}

export function StatusBadge({ status }: { status: InitiativeStatus }) {
  const { t } = useTranslation("settings");
  return (
    <span
      className={cn(
        "shrink-0 rounded-full px-2 py-0.5 text-xs font-medium",
        STATUS_COLORS[status] ?? STATUS_COLORS.active,
      )}
    >
      {t(`initiatives.statusBadge.${status}`, { defaultValue: status })}
    </span>
  );
}

export function InitiativeProgress({ initiative }: { initiative: Initiative }) {
  const { t } = useTranslation("settings");
  const total = initiative.total_tasks ?? 0;
  const completed = initiative.completed_tasks ?? 0;
  const pct = progressPct(initiative);

  if (initiative.status !== "completed" && total === 0) {
    return <span className="text-xs text-muted-foreground">{t("initiatives.progress.noTasks")}</span>;
  }

  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2">
        <div
          className="h-2 flex-1 rounded-full bg-muted"
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct}
        >
          <div className="h-2 rounded-full bg-primary transition-all" style={{ width: `${pct}%` }} />
        </div>
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{pct}%</span>
      </div>
      <p className="text-xs text-muted-foreground">
        {initiative.status === "completed"
          ? t("initiatives.progress.completed")
          : t("initiatives.progress.tasksDone", { completed, total })}
      </p>
    </div>
  );
}

/** A card that is a real link to the initiative page (Ctrl+click opens a tab). */
export function InitiativeCard({
  initiative,
  href,
  layout = "grid",
}: {
  initiative: Initiative;
  href: string;
  layout?: "grid" | "list";
}) {
  const { t } = useTranslation("settings");
  const targetDate = formatTargetDate(initiative.target_date);

  if (layout === "list") {
    return (
      <Link
        to={href}
        className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border bg-card px-3 py-2 text-sm transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Target className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate font-medium">{initiative.name}</span>
        <StatusBadge status={initiative.status} />
        <span className="w-12 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
          {progressPct(initiative)}%
        </span>
        {targetDate && (
          <span className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
            <Calendar className="h-3 w-3" />
            {targetDate}
          </span>
        )}
        {initiative.description && (
          <p className="w-full truncate text-xs text-muted-foreground">{initiative.description}</p>
        )}
      </Link>
    );
  }

  return (
    <Link
      to={href}
      className="block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Card className="h-full transition-shadow hover:shadow-md">
        <CardHeader className="pb-2">
          <div className="flex items-start justify-between gap-2">
            <div className="flex min-w-0 flex-1 items-center gap-2">
              <Target className="h-4 w-4 shrink-0 text-muted-foreground" />
              <CardTitle className="truncate text-base">{initiative.name}</CardTitle>
            </div>
            <StatusBadge status={initiative.status} />
          </div>
          {initiative.description && (
            <p className="line-clamp-2 text-sm text-muted-foreground">{initiative.description}</p>
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
    </Link>
  );
}

/**
 * Create (no `initiative`) or edit (with it) form. Create may link one project
 * right away; edit goes through the existing PATCH /initiatives/:id.
 */
export function InitiativeForm({
  workspaceId,
  projects,
  initiative,
  onDone,
  onCancel,
}: {
  workspaceId: string;
  projects: Project[];
  initiative?: Initiative;
  onDone: (saved: Initiative) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation("settings");
  const { createInitiative, updateInitiative, linkProject } = useInitiativeStore();
  const editing = !!initiative;
  const [name, setName] = useState(initiative?.name ?? "");
  const [description, setDescription] = useState(initiative?.description ?? "");
  const [status, setStatus] = useState<InitiativeStatus>(initiative?.status ?? "active");
  const [targetDate, setTargetDate] = useState<string | null>(initiative?.target_date?.slice(0, 10) ?? null);
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
        let saved: Initiative;
        if (initiative) {
          saved = await updateInitiative(initiative.id, req);
          toast.success(t("initiatives.toast.updated"));
        } else {
          saved = await createInitiative(workspaceId, req);
          if (projectId) await linkProject(saved.id, projectId);
          toast.success(t("initiatives.toast.created"));
        }
        onDone(saved);
      } catch (err) {
        toast.error(
          apiErrorMessage(err, t(editing ? "initiatives.toast.updateFailed" : "initiatives.toast.createFailed")),
        );
      } finally {
        setSubmitting(false);
      }
    },
    [name, description, status, targetDate, projectId, workspaceId, initiative, editing,
      createInitiative, updateInitiative, linkProject, onDone, t],
  );

  return (
    <Card className="border-primary/30">
      <CardHeader className="pb-3">
        <CardTitle className="text-base">
          {t(editing ? "initiatives.form.editHeading" : "initiatives.form.heading")}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1">
            <label className="text-sm font-medium" htmlFor="initiative-name">{t("initiatives.form.name")}</label>
            <Input
              id="initiative-name"
              placeholder={t("initiatives.form.namePlaceholder")}
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              autoFocus
            />
          </div>
          <div className="space-y-1">
            <label className="text-sm font-medium" htmlFor="initiative-description">
              {t("initiatives.form.description")}
            </label>
            <Textarea
              id="initiative-description"
              placeholder={t("initiatives.form.descriptionPlaceholder")}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
            />
          </div>
          {!editing && (
            <div className="space-y-1">
              <label className="text-sm font-medium" htmlFor="initiative-project">{t("initiatives.form.project")}</label>
              <Select id="initiative-project" value={projectId} onChange={(e) => setProjectId(e.target.value)}>
                <option value="">{t("initiatives.form.noProject")}</option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </Select>
            </div>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1">
              <label className="text-sm font-medium" htmlFor="initiative-status">{t("initiatives.form.status")}</label>
              <Select
                id="initiative-status"
                value={status}
                onChange={(e) => setStatus(e.target.value as InitiativeStatus)}
              >
                {INITIATIVE_STATUSES.map((s) => (
                  <option key={s} value={s}>{t(`initiatives.status.${s}`)}</option>
                ))}
              </Select>
            </div>
            <div className="space-y-1">
              <label className="text-sm font-medium" htmlFor="initiative-target">{t("initiatives.form.targetDate")}</label>
              <Input
                id="initiative-target"
                type="date"
                value={targetDate ?? ""}
                onChange={(e) => setTargetDate(e.target.value || null)}
              />
            </div>
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onCancel}>
              {t("initiatives.form.cancel")}
            </Button>
            <Button type="submit" disabled={submitting}>
              {editing
                ? submitting ? t("initiatives.form.saving") : t("initiatives.form.save")
                : submitting ? t("initiatives.form.creating") : t("initiatives.form.create")}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

/**
 * Linked projects with link and unlink, both only with `rights.link`. The
 * server lists only the projects the viewer may open, and links only those.
 */
export function LinkedProjects({
  initiative,
  projects,
  rights,
  onChanged,
  compact = false,
}: {
  initiative: Initiative;
  projects: Project[];
  rights: InitiativeRights;
  onChanged: () => void;
  compact?: boolean;
}) {
  const { t } = useTranslation("settings");
  const { linkProject, unlinkProject } = useInitiativeStore();
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const linked = initiative.linked_projects ?? [];
  const linkedIds = new Set(linked.map((p) => p.id));
  const available = projects.filter((p) => !linkedIds.has(p.id));

  const handleLink = useCallback(async () => {
    if (!selected) return;
    setBusy(true);
    try {
      await linkProject(initiative.id, selected);
      setSelected("");
      toast.success(t("initiatives.toast.linked"));
      onChanged();
    } catch (err) {
      toast.error(apiErrorMessage(err, t("initiatives.toast.linkFailed")));
    } finally {
      setBusy(false);
    }
  }, [selected, initiative.id, linkProject, onChanged, t]);

  const handleUnlink = useCallback(
    async (projectId: string) => {
      try {
        await unlinkProject(initiative.id, projectId);
        toast.success(t("initiatives.toast.unlinked"));
        onChanged();
      } catch (err) {
        toast.error(apiErrorMessage(err, t("initiatives.toast.unlinkFailed")));
      }
    },
    [initiative.id, unlinkProject, onChanged, t],
  );

  return (
    <div className="space-y-3">
      {rights.link && available.length > 0 && (
        <div className="flex flex-wrap gap-2">
          <Select
            value={selected}
            onChange={(e) => setSelected(e.target.value)}
            className="min-w-0 flex-1 basis-40"
            aria-label={t("initiatives.detail.linkProject")}
          >
            <option value="">{t("initiatives.detail.selectProject")}</option>
            {available.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </Select>
          <Button onClick={handleLink} disabled={!selected || busy} size={compact ? "sm" : "default"}>
            <Link2 className="h-4 w-4" />
            {t("initiatives.detail.link")}
          </Button>
        </div>
      )}
      <h3 className={cn("font-semibold", compact ? "text-sm" : "text-lg")}>
        {t("initiatives.detail.linkedProjects", { n: linked.length })}
      </h3>
      {linked.length === 0 ? (
        <div className="flex flex-col items-center rounded-md border border-dashed py-6 text-center">
          <FolderKanban className="mb-2 h-6 w-6 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">{t("initiatives.detail.noLinked")}</p>
        </div>
      ) : (
        <ul className="space-y-2">
          {linked.map((project) => (
            <li key={project.id} className="flex items-center gap-3 rounded-md border bg-card px-3 py-2">
              <EntityIcon value={project.icon} fallback={FolderKanban} className="h-4 w-4 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">{project.name}</p>
                {!compact && project.description && (
                  <p className="truncate text-xs text-muted-foreground">{project.description}</p>
                )}
              </div>
              {rights.link && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 shrink-0"
                  onClick={() => void handleUnlink(project.id)}
                  aria-label={t("initiatives.detail.unlink", { name: project.name })}
                  title={t("initiatives.detail.unlink", { name: project.name })}
                >
                  <Link2Off className="h-3.5 w-3.5" />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
