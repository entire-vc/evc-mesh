import { useState, useEffect, useCallback } from "react";
import {
  GitBranch,
  GitCommit,
  GitPullRequest,
  Link,
  Plus,
  Trash2,
  ExternalLink,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/cn";
import type { VCSLink, VCSLinkType, VCSProvider, CreateVCSLinkRequest } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";
import { formatDateTime, formatRelative } from "@/lib/utils";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const LINK_TYPE_CONFIG: Record<
  VCSLinkType,
  { icon: typeof GitPullRequest; color: string }
> = {
  pr: { icon: GitPullRequest, color: "text-violet-500" },
  commit: { icon: GitCommit, color: "text-amber-500" },
  branch: { icon: GitBranch, color: "text-teal-500" },
};

const PR_STATUS_CONFIG: Record<
  string,
  { className: string }
> = {
  open: { className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200" },
  merged: { className: "bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-200" },
  closed: { className: "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200" },
};

/**
 * How fresh an OPEN PR link's status is. An "open" badge is only as true as
 * the last webhook that confirmed it — a lost "closed"/"merged" delivery used
 * to leave it open forever with nothing hinting at it. Merged/closed are
 * terminal, so they get no hint.
 */
export function PRSyncHint({ link }: { link: VCSLink }) {
  const { t } = useTranslation("task");
  if (link.link_type !== "pr" || link.status !== "open") return null;
  const syncedAt = link.status_synced_at;
  if (!syncedAt) {
    return (
      <span
        className="text-muted-foreground"
        title={t("vcs.sync.neverConfirmed")}
        data-testid="pr-sync-hint"
      >
        {t("vcs.sync.notVerified")}
      </span>
    );
  }
  return (
    <span
      className="text-muted-foreground"
      title={t("vcs.sync.lastConfirmed", { date: formatDateTime(syncedAt) })}
      data-testid="pr-sync-hint"
    >
      {t("vcs.sync.synced", { when: formatRelative(syncedAt) })}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

interface VCSLinksProps {
  taskId: string;
  /** Show (and add) only this provider's links: the body of an extension `task.panel`. */
  only?: VCSProvider;
  /** Called once the first links request has settled (ok or failed). */
  onLoaded?: () => void;
  /** Hide providers whose links an extension panel owns. Ignored when `only` is set. */
  exclude?: readonly VCSProvider[];
}

const ALL_PROVIDERS: { id: VCSProvider; label: string }[] = [
  { id: "github", label: "GitHub" },
  { id: "gitlab", label: "GitLab" },
];

export function VCSLinks({ taskId, only, exclude = [], onLoaded }: VCSLinksProps) {
  const { t } = useTranslation("task");
  const [allLinks, setLinks] = useState<VCSLink[]>([]);
  const providerChoices = only
    ? ALL_PROVIDERS.filter((p) => p.id === only)
    : ALL_PROVIDERS.filter((p) => !exclude.includes(p.id));
  const defaultProvider: VCSProvider = providerChoices[0]?.id ?? "github";
  const links = allLinks.filter((l) => (only ? l.provider === only : !exclude.includes(l.provider)));
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [form, setForm] = useState<CreateVCSLinkRequest>({
    link_type: "pr",
    external_id: "",
    url: "",
    title: "",
    provider: defaultProvider,
  });

  const fetchLinks = useCallback(async () => {
    try {
      const res = await api<{ vcs_links: VCSLink[] }>(
        `/api/v1/tasks/${taskId}/vcs-links`,
      );
      setLinks(res.vcs_links ?? []);
    } catch {
      // Non-fatal, just show empty state
    } finally {
      setLoading(false);
      onLoaded?.();
    }
  }, [taskId, onLoaded]);

  useEffect(() => {
    void fetchLinks();
  }, [fetchLinks]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.url || !form.external_id) {
      setError(t("vcs.required"));
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const newLink = await api<VCSLink>(
        `/api/v1/tasks/${taskId}/vcs-links`,
        { method: "POST", body: form },
      );
      setLinks((prev) => [...prev, newLink]);
      setShowForm(false);
      setForm({ link_type: "pr", external_id: "", url: "", title: "", provider: defaultProvider });
    } catch (err: unknown) {
      setError(apiErrorMessage(err, t("vcs.createFailed")));
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (linkId: string) => {
    try {
      await api(`/api/v1/vcs-links/${linkId}`, { method: "DELETE" });
      setLinks((prev) => prev.filter((l) => l.id !== linkId));
    } catch {
      // ignore
    }
  };

  if (loading) {
    return (
      <div className="space-y-2">
        <div className="h-4 w-32 animate-pulse rounded bg-muted" />
        <div className="h-10 w-full animate-pulse rounded bg-muted" />
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Link className="h-4 w-4" />
          {t("vcs.title")}
          {links.length > 0 && (
            <span className="text-xs text-muted-foreground">({links.length})</span>
          )}
        </div>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1 text-xs"
          onClick={() => setShowForm((v) => !v)}
        >
          <Plus className="h-3 w-3" />
          {t("vcs.linkPr")}
        </Button>
      </div>

      {/* Existing links */}
      {links.length > 0 && (
        <div className="space-y-2">
          {links.map((link) => {
            const cfg = LINK_TYPE_CONFIG[link.link_type] ?? LINK_TYPE_CONFIG.commit;
            const Icon = cfg.icon;
            const statusCfg = link.status ? PR_STATUS_CONFIG[link.status] : null;
            return (
              <div
                key={link.id}
                className="group flex items-start gap-3 rounded-lg border border-border bg-card p-3"
              >
                <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", cfg.color)} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <a
                      href={link.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="flex items-center gap-1 truncate text-sm font-medium hover:underline"
                    >
                      {link.title || link.url}
                      <ExternalLink className="h-3 w-3 shrink-0 opacity-50" />
                    </a>
                    {statusCfg && (
                      <Badge
                        className={cn("shrink-0 text-[10px] px-1.5 py-0", statusCfg.className)}
                      >
                        {t(`vcs.status.${link.status}`)}
                      </Badge>
                    )}
                  </div>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {t(`vcs.type.${link.link_type in LINK_TYPE_CONFIG ? link.link_type : "commit"}`)} #{link.external_id} &middot; {link.provider}
                    {link.status === "open" && link.link_type === "pr" && (<> &middot; <PRSyncHint link={link} /></>)}
                  </p>
                </div>
                <button
                  onClick={() => void handleDelete(link.id)}
                  className="mt-0.5 shrink-0 opacity-0 transition-opacity group-hover:opacity-100 hover:text-destructive"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </div>
            );
          })}
        </div>
      )}

      {links.length === 0 && !showForm && (
        <p className="text-xs text-muted-foreground">{t("vcs.empty")}</p>
      )}

      {/* Add link form */}
      {showForm && (
        <form onSubmit={(e) => void handleSubmit(e)} className="space-y-2 rounded-lg border border-border p-3">
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-xs text-muted-foreground">{t("vcs.form.type")}</label>
              <Select
                value={form.link_type}
                onChange={(e) =>
                  setForm((f) => ({ ...f, link_type: e.target.value as VCSLinkType }))
                }
                className="mt-1"
              >
                <option value="pr">{t("vcs.type.pr")}</option>
                <option value="commit">{t("vcs.type.commit")}</option>
                <option value="branch">{t("vcs.type.branch")}</option>
              </Select>
            </div>
            <div>
              <label className="text-xs text-muted-foreground">{t("vcs.form.provider")}</label>
              <Select
                value={form.provider}
                onChange={(e) =>
                  setForm((f) => ({ ...f, provider: e.target.value as VCSProvider }))
                }
                className="mt-1"
              >
                {providerChoices.map((p) => (
                  <option key={p.id} value={p.id}>{p.label}</option>
                ))}
              </Select>
            </div>
          </div>
          <div>
            <label className="text-xs text-muted-foreground">URL</label>
            <Input
              className="mt-1"
              placeholder={form.provider === "gitlab" ? "https://gitlab.example.com/group/repo/-/merge_requests/123" : "https://github.com/org/repo/pull/123"}
              value={form.url}
              onChange={(e) => setForm((f) => ({ ...f, url: e.target.value }))}
            />
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-xs text-muted-foreground">ID / SHA</label>
              <Input
                className="mt-1"
                placeholder={t("vcs.form.idPlaceholder")}
                value={form.external_id}
                onChange={(e) =>
                  setForm((f) => ({ ...f, external_id: e.target.value }))
                }
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">{t("vcs.form.title")}</label>
              <Input
                className="mt-1"
                placeholder={t("vcs.form.titlePlaceholder")}
                value={form.title}
                onChange={(e) => setForm((f) => ({ ...f, title: e.target.value }))}
              />
            </div>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex gap-2">
            <Button type="submit" size="sm" disabled={submitting} className="flex-1">
              {submitting ? t("vcs.form.linking") : t("vcs.form.submit")}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                setShowForm(false);
                setError(null);
              }}
            >
              {t("actions.cancel")}
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
