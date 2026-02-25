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
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/cn";
import type { VCSLink, VCSLinkType, CreateVCSLinkRequest } from "@/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const LINK_TYPE_CONFIG: Record<
  VCSLinkType,
  { label: string; icon: typeof GitPullRequest; color: string }
> = {
  pr: { label: "Pull Request", icon: GitPullRequest, color: "text-violet-500" },
  commit: { label: "Commit", icon: GitCommit, color: "text-amber-500" },
  branch: { label: "Branch", icon: GitBranch, color: "text-teal-500" },
};

const PR_STATUS_CONFIG: Record<
  string,
  { label: string; className: string }
> = {
  open: { label: "Open", className: "bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200" },
  merged: { label: "Merged", className: "bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-200" },
  closed: { label: "Closed", className: "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200" },
};

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

interface VCSLinksProps {
  taskId: string;
}

export function VCSLinks({ taskId }: VCSLinksProps) {
  const [links, setLinks] = useState<VCSLink[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [form, setForm] = useState<CreateVCSLinkRequest>({
    link_type: "pr",
    external_id: "",
    url: "",
    title: "",
    provider: "github",
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
    }
  }, [taskId]);

  useEffect(() => {
    void fetchLinks();
  }, [fetchLinks]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.url || !form.external_id) {
      setError("URL and external ID are required.");
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
      setForm({ link_type: "pr", external_id: "", url: "", title: "", provider: "github" });
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create link.");
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
          VCS Links
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
          Link PR
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
                        {statusCfg.label}
                      </Badge>
                    )}
                  </div>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {cfg.label} #{link.external_id} &middot; {link.provider}
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
        <p className="text-xs text-muted-foreground">No VCS links yet.</p>
      )}

      {/* Add link form */}
      {showForm && (
        <form onSubmit={(e) => void handleSubmit(e)} className="space-y-2 rounded-lg border border-border p-3">
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-xs text-muted-foreground">Type</label>
              <Select
                value={form.link_type}
                onChange={(e) =>
                  setForm((f) => ({ ...f, link_type: e.target.value as VCSLinkType }))
                }
                className="mt-1"
              >
                <option value="pr">Pull Request</option>
                <option value="commit">Commit</option>
                <option value="branch">Branch</option>
              </Select>
            </div>
            <div>
              <label className="text-xs text-muted-foreground">Provider</label>
              <Select
                value={form.provider}
                onChange={(e) =>
                  setForm((f) => ({ ...f, provider: e.target.value as "github" | "gitlab" }))
                }
                className="mt-1"
              >
                <option value="github">GitHub</option>
                <option value="gitlab">GitLab</option>
              </Select>
            </div>
          </div>
          <div>
            <label className="text-xs text-muted-foreground">URL</label>
            <Input
              className="mt-1"
              placeholder="https://github.com/org/repo/pull/123"
              value={form.url}
              onChange={(e) => setForm((f) => ({ ...f, url: e.target.value }))}
            />
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-xs text-muted-foreground">ID / SHA</label>
              <Input
                className="mt-1"
                placeholder="123 or abc123ef"
                value={form.external_id}
                onChange={(e) =>
                  setForm((f) => ({ ...f, external_id: e.target.value }))
                }
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">Title (optional)</label>
              <Input
                className="mt-1"
                placeholder="Fix authentication bug"
                value={form.title}
                onChange={(e) => setForm((f) => ({ ...f, title: e.target.value }))}
              />
            </div>
          </div>
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex gap-2">
            <Button type="submit" size="sm" disabled={submitting} className="flex-1">
              {submitting ? "Linking..." : "Add Link"}
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
              Cancel
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
