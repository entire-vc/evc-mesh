import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Link2, Plus, Unlink } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";
import {
  deleteProjectExtension, listProjectRepos, saveProjectExtension,
  type ProjectBinding, type ProjectRepo, type ProjectRepoSource,
} from "@/lib/extension-settings";
import { apiErrorMessage } from "@/lib/api-error";
import * as i18nFmt from "@/i18n/format";

/** Past this many rows the list gets a name filter. */
const FILTER_FROM = 8;

const SOURCE_VARIANT: Record<ProjectRepoSource, "secondary" | "success" | "default"> = {
  webhook: "secondary", binding: "success", both: "default",
};

/** The settings field a Bind writes the repository to: the manifest's first required project field. */
export function repoField(entry: CatalogEntry | undefined): string {
  const schema = entry?.manifest.settings?.project as { required?: string[]; properties?: Record<string, unknown> } | undefined;
  return schema?.required?.[0] ?? Object.keys(schema?.properties ?? {})[0] ?? (entry?.id === "gitlab" ? "repo_path" : "repo");
}

const repoOf = (binding: ProjectBinding | undefined, field: string) => {
  const v = binding?.settings?.[field];
  return typeof v === "string" ? v.trim() : "";
};

/**
 * Repositories a GitLab/GitHub extension knows for this project: those whose
 * task links arrive through the instance webhook and the one bound to the
 * project, each with where it comes from, and Bind/Unbind for a manager.
 */
export function VcsRepos({
  projectId, entry, binding, canBind, canManage, onChanged,
}: {
  projectId: string; entry: CatalogEntry; binding: ProjectBinding | undefined;
  canBind: boolean; canManage: boolean; onChanged: () => void;
}) {
  const { t } = useTranslation("integrations");
  const id = entry.id;
  const field = repoField(entry);
  const boundRepo = repoOf(binding, field);
  const [repos, setRepos] = useState<ProjectRepo[] | null>(null);
  const [failed, setFailed] = useState(false);
  // The repos endpoint is not on this server yet: show the binding alone.
  const [partial, setPartial] = useState(false);
  const [filter, setFilter] = useState("");
  // Bind form open; the string is the prefilled repository.
  const [bindDraft, setBindDraft] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const latest = useRef(0);
  const load = useCallback(() => {
    const seq = ++latest.current;
    setFailed(false);
    listProjectRepos(projectId, id)
      .then((list) => { if (seq === latest.current) { setPartial(false); setRepos(list); } })
      .catch((err) => {
        if (seq !== latest.current) return;
        if ((err as { status?: number })?.status === 404) {
          setPartial(true);
          setRepos(boundRepo ? [{ repo: boundRepo, host: "", links: 0, bound: true, source: "binding" }] : []);
        } else setFailed(true);
      });
  }, [projectId, id, boundRepo]);
  useEffect(() => { load(); }, [load]);

  const after = () => { load(); onChanged(); };

  const bind = async (repo: string) => {
    setBusy(true);
    try {
      await saveProjectExtension(projectId, id, {
        enabled: binding?.enabled ?? true, settings: { ...(binding?.settings ?? {}), [field]: repo },
      });
      toast.success(t("projectTab.repos.bound", { repo }));
      setBindDraft(null);
      after();
    } catch (err) { toast.error(apiErrorMessage(err, t("projectTab.actionFailed"))); } finally { setBusy(false); }
  };
  const unbind = async () => {
    setBusy(true);
    try {
      await deleteProjectExtension(projectId, id);
      toast.success(t("projectTab.repos.unbound", { repo: confirm }));
      setConfirm(null);
      after();
    } catch (err) { toast.error(apiErrorMessage(err, t("projectTab.actionFailed"))); } finally { setBusy(false); }
  };

  const manage = canManage && canBind;
  const q = filter.trim().toLowerCase();
  const shown = (repos ?? []).filter((r) => !q || r.repo.toLowerCase().includes(q));

  return (
    <Card data-testid={`vcs-repos-${id}`}>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle className="text-base">{t("projectTab.repos.title")}</CardTitle>
          {manage && bindDraft === null && (
            <Button variant="outline" size="sm" onClick={() => setBindDraft("")}>
              <Plus className="mr-1.5 h-4 w-4" aria-hidden />{t("projectTab.bindRepo")}
            </Button>
          )}
        </div>
        <CardDescription>{t("projectTab.repos.sources")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {bindDraft !== null && (
          <BindForm key={bindDraft} initial={bindDraft} label={t("projectTab.repos.field")} replaces={boundRepo}
            busy={busy} onCancel={() => setBindDraft(null)} onSubmit={bind} />
        )}
        {failed ? (
          <div className="flex flex-col items-start gap-2" data-testid={`vcs-repos-error-${id}`}>
            <p role="alert" className="flex items-center gap-2 text-sm text-destructive">
              <AlertTriangle className="h-4 w-4" aria-hidden />{t("projectTab.repos.loadFailed")}
            </p>
            <Button variant="outline" size="sm" onClick={load}>{t("projectTab.retry")}</Button>
          </div>
        ) : repos === null ? (
          <div className="space-y-2" data-testid={`vcs-repos-loading-${id}`}>
            <Skeleton className="h-12 w-full" /><Skeleton className="h-12 w-full" />
          </div>
        ) : repos.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid={`vcs-repos-empty-${id}`}>
            {t(partial ? "projectTab.repos.partialEmpty" : "projectTab.repos.empty")}
          </p>
        ) : (
          <>
            {repos.length > FILTER_FROM && (
              <Input value={filter} onChange={(e) => setFilter(e.target.value)}
                aria-label={t("projectTab.repos.filter")} placeholder={t("projectTab.repos.filter")} />
            )}
            <ul className="divide-y divide-border rounded-lg border border-border" data-testid={`vcs-repos-list-${id}`}>
              {shown.map((r) => (
                <li key={`${r.host}/${r.repo}`} data-testid={`vcs-repo-${r.repo}`}
                  className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="break-all font-medium">{r.repo}</span>
                      <Badge variant={SOURCE_VARIANT[r.source]} data-testid="repo-source">{t(`projectTab.repos.source.${r.source}`)}</Badge>
                    </div>
                    <p className="text-xs text-muted-foreground">
                      {[
                        r.host,
                        r.links > 0 ? t("projectTab.repos.links", { count: r.links, n: i18nFmt.formatNumber(r.links) }) : t("projectTab.repos.noLinks"),
                        r.last_event_at ? t("projectTab.repos.lastEvent", { when: i18nFmt.formatDateTime(r.last_event_at) }) : null,
                      ].filter(Boolean).join(" · ")}
                    </p>
                  </div>
                  {manage && (r.bound ? (
                    <Button variant="outline" size="sm" className="w-full sm:w-auto" disabled={busy}
                      onClick={() => setConfirm(r.repo)} aria-label={t("projectTab.repos.unbindRepo", { repo: r.repo })}>
                      <Unlink className="mr-1.5 h-4 w-4" aria-hidden />{t("projectTab.unbind")}
                    </Button>
                  ) : (
                    <Button variant="outline" size="sm" className="w-full sm:w-auto" disabled={busy}
                      onClick={() => setBindDraft(r.repo)} aria-label={t("projectTab.repos.bindRepo", { repo: r.repo })}>
                      <Link2 className="mr-1.5 h-4 w-4" aria-hidden />{t("projectTab.bind")}
                    </Button>
                  ))}
                </li>
              ))}
              {shown.length === 0 && <li className="p-3 text-sm text-muted-foreground">{t("projectTab.search.empty")}</li>}
            </ul>
          </>
        )}
        {partial && repos !== null && repos.length > 0 && <p className="text-xs text-muted-foreground" data-testid={`vcs-repos-partial-${id}`}>{t("projectTab.repos.partial")}</p>}
      </CardContent>
      <Dialog open={confirm !== null} onOpenChange={(o) => { if (!o) setConfirm(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("projectTab.repos.unbindTitle", { repo: confirm })}</DialogTitle>
            <DialogDescription>{t("projectTab.repos.unbindDesc")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirm(null)}>{t("projectTab.cancel")}</Button>
            <Button variant="destructive" onClick={unbind} disabled={busy}>{t("projectTab.unbind")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function BindForm({
  initial, label, replaces, busy, onCancel, onSubmit,
}: { initial: string; label: string; replaces: string; busy: boolean; onCancel: () => void; onSubmit: (repo: string) => void }) {
  const { t } = useTranslation("integrations");
  const [repo, setRepo] = useState(initial);
  const value = repo.trim();
  return (
    <form className="space-y-2 rounded-lg border border-border p-3" data-testid="vcs-bind-form"
      onSubmit={(e) => { e.preventDefault(); if (value) onSubmit(value); }}>
      <label className="text-sm font-medium" htmlFor="vcs-bind-repo">{label}</label>
      <Input id="vcs-bind-repo" value={repo} onChange={(e) => setRepo(e.target.value)} autoFocus placeholder="group/project" />
      {replaces && replaces !== value && (
        <p className="text-xs text-muted-foreground">{t("projectTab.repos.replaces", { repo: replaces })}</p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" disabled={busy || !value}>{t("projectTab.bind")}</Button>
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>{t("projectTab.cancel")}</Button>
      </div>
    </form>
  );
}
