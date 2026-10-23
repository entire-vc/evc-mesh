import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import {
  Brain,
  ChevronDown,
  ChevronUp,
  Plus,
  Search,
  SlidersHorizontal,
  Trash2,
  X,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemoryStore } from "@/stores/memory";
import { useAgentStore } from "@/stores/agent";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { MarkdownView } from "@/components/markdown-view";
import {
  MemoryFiltersPanel,
  countActiveFilters,
  emptyFilter,
} from "@/components/memory/memory-filters";
import type { Memory, MemoryFilter, MemoryOrderBy } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function scopeBadgeClass(scope: Memory["scope"]): string {
  switch (scope) {
    case "workspace":
      return "bg-tone-info-100 text-tone-info-700 dark:bg-tone-info-900 dark:text-tone-info-300";
    case "project":
      return "bg-tone-success-100 text-tone-success-700 dark:bg-tone-success-900 dark:text-tone-success-300";
    case "agent":
      return "bg-tone-purple-100 text-tone-purple-700 dark:bg-tone-purple-900 dark:text-tone-purple-300";
  }
}


function relevanceBarClass(relevance: number): string {
  if (relevance >= 0.7) return "bg-tone-success-500";
  if (relevance >= 0.3) return "bg-tone-attention-500";
  return "bg-tone-danger-400";
}

// ---------------------------------------------------------------------------
// MemoryCard
// ---------------------------------------------------------------------------

interface MemoryCardProps {
  memory: Memory & { score?: number };
  onDelete: (id: string) => void;
}

function MemoryCard({ memory, onDelete }: MemoryCardProps) {
  const { t } = useTranslation("misc");
  const [expanded, setExpanded] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const isStale = memory.relevance < 0.3;

  return (
    <Card
      className={cn(
        "p-4 transition-colors",
        isStale && "border-warning/40 bg-warning/5",
      )}
    >
      <div className="flex items-start justify-between gap-3">
        {/* Left: key + content */}
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 mb-1">
            <code className="text-sm font-semibold font-mono text-foreground">
              {memory.key}
            </code>
            <span
              className={cn(
                "inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium",
                scopeBadgeClass(memory.scope),
              )}
            >
              {t(`memory.scopeBadge.${memory.scope}`)}
            </span>
            {memory.score !== undefined && (
              <span className="inline-flex items-center rounded-md bg-muted px-2 py-0.5 text-xs text-muted-foreground">
                {t("memory.card.score", { score: memory.score.toFixed(3) })}
              </span>
            )}
          </div>

          {/* Content */}
          <div
            className={cn(
              "text-sm text-foreground",
              !expanded && "line-clamp-3",
            )}
          >
            <MarkdownView content={memory.content} />
          </div>

          {/* Expand / collapse toggle */}
          <button
            onClick={() => setExpanded((v) => !v)}
            className="mt-1 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
          >
            {expanded ? (
              <>
                <ChevronUp className="h-3 w-3" /> {t("memory.card.showLess")}
              </>
            ) : (
              <>
                <ChevronDown className="h-3 w-3" /> {t("memory.card.showMore")}
              </>
            )}
          </button>

          {/* Tags */}
          {memory.tags && memory.tags.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-1">
              {memory.tags.map((tag) => (
                <Badge key={tag} variant="outline" className="text-xs">
                  {tag}
                </Badge>
              ))}
            </div>
          )}

          {/* Relevance bar + metadata */}
          <div className="mt-3 flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-1.5">
              <span className="text-xs text-muted-foreground">{t("memory.card.relevance")}</span>
              <div className="h-1.5 w-16 rounded-full bg-muted overflow-hidden">
                <div
                  className={cn("h-full rounded-full", relevanceBarClass(memory.relevance))}
                  style={{ width: `${Math.round(memory.relevance * 100)}%` }}
                />
              </div>
              <span className="text-xs text-muted-foreground">
                {Math.round(memory.relevance * 100)}%
              </span>
            </div>
            <span className="text-xs text-muted-foreground">
              {t(`memory.source.${memory.source_type}`)} &middot; {t("memory.card.updated")}{" "}
              {formatRelative(memory.updated_at)}
            </span>
            {isStale && (
              <Badge variant="warning" className="text-xs">
                {t("memory.card.stale")}
              </Badge>
            )}
            {memory.archived && (
              <Badge variant="secondary" className="text-xs text-muted-foreground">
                {t("memory.card.archived")}
              </Badge>
            )}
          </div>
        </div>

        {/* Right: delete */}
        <div className="shrink-0">
          {confirmDelete ? (
            <div className="flex items-center gap-1">
              <Button
                size="sm"
                variant="destructive"
                onClick={() => onDelete(memory.id)}
              >
                {t("memory.card.confirm")}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setConfirmDelete(false)}
              >
                <X className="h-3 w-3" />
              </Button>
            </div>
          ) : (
            <Button
              size="icon"
              variant="ghost"
              className="h-7 w-7 text-muted-foreground hover:text-destructive"
              onClick={() => setConfirmDelete(true)}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
      </div>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// CreateMemoryForm
// ---------------------------------------------------------------------------

interface CreateMemoryFormProps {
  onSubmit: (data: Partial<Memory>) => Promise<void>;
  workspaceId: string;
}

function CreateMemoryForm({ onSubmit, workspaceId }: CreateMemoryFormProps) {
  const { t } = useTranslation("misc");
  const [key, setKey] = useState("");
  const [content, setContent] = useState("");
  const [scope, setScope] = useState<Memory["scope"]>("workspace");
  const [tagsRaw, setTagsRaw] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!key.trim() || !content.trim()) return;
    setSubmitting(true);
    setError(null);
    try {
      const tags = tagsRaw
        .split(",")
        .map((t) => t.trim())
        .filter(Boolean);
      await onSubmit({
        workspace_id: workspaceId,
        key: key.trim(),
        content: content.trim(),
        scope,
        tags,
      });
      setKey("");
      setContent("");
      setTagsRaw("");
      setScope("workspace");
    } catch (err) {
      setError(apiErrorMessage(err, t("memory.form.createFailed")));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label className="text-xs font-medium text-muted-foreground mb-1 block">
            {t("memory.form.key")}
          </label>
          <Input
            placeholder={t("memory.form.keyPlaceholder")}
            value={key}
            onChange={(e) => setKey(e.target.value)}
            pattern="[a-z0-9_\-]+"
            title={t("memory.form.keyTitle")}
            required
          />
        </div>
        <div>
          <label className="text-xs font-medium text-muted-foreground mb-1 block">
            {t("memory.form.scope")}
          </label>
          <Select
            value={scope}
            onChange={(e) => setScope(e.target.value as Memory["scope"])}
          >
            <option value="workspace">{t("memory.scope.workspace")}</option>
            <option value="project">{t("memory.scope.project")}</option>
            <option value="agent">{t("memory.scope.agent")}</option>
          </Select>
        </div>
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground mb-1 block">
          {t("memory.form.content")}
        </label>
        <Textarea
          placeholder={t("memory.form.contentPlaceholder")}
          value={content}
          onChange={(e) => setContent(e.target.value)}
          className="min-h-[80px]"
          required
        />
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground mb-1 block">
          {t("memory.form.tags")}
        </label>
        <Input
          placeholder={t("memory.form.tagsPlaceholder")}
          value={tagsRaw}
          onChange={(e) => setTagsRaw(e.target.value)}
        />
      </div>

      {error && <p className="text-xs text-destructive">{error}</p>}

      <div className="flex justify-end">
        <Button type="submit" disabled={submitting || !key.trim() || !content.trim()}>
          {submitting ? t("memory.form.saving") : t("memory.form.save")}
        </Button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// URL <-> filter serialization (shareable links)
// ---------------------------------------------------------------------------

function filterFromParams(sp: URLSearchParams): MemoryFilter {
  const tagsRaw = sp.get("tags") ?? "";
  const relRaw = sp.get("rel_min");
  return {
    q: sp.get("q") ?? "",
    scope: sp.get("scope") ?? "all",
    tags: tagsRaw ? tagsRaw.split(",").filter(Boolean) : [],
    tagsMode: sp.get("tags_mode") === "any" ? "any" : "all",
    sourceType: (sp.get("source") as MemoryFilter["sourceType"]) ?? "",
    createdBy: sp.get("agent") ?? "",
    since: sp.get("since") ?? "",
    until: sp.get("until") ?? "",
    relevanceMin: relRaw ? Number(relRaw) : 0,
    includeExpired: sp.get("expired") === "1",
    includeArchived: sp.get("archived") === "1",
    orderBy: (sp.get("sort") as MemoryOrderBy) ?? "created_at:desc",
  };
}

function paramsFromFilter(f: MemoryFilter): URLSearchParams {
  const sp = new URLSearchParams();
  if (f.q) sp.set("q", f.q);
  if (f.scope && f.scope !== "all") sp.set("scope", f.scope);
  if (f.tags && f.tags.length > 0) {
    sp.set("tags", f.tags.join(","));
    if (f.tagsMode === "any") sp.set("tags_mode", "any");
  }
  if (f.sourceType) sp.set("source", f.sourceType);
  if (f.sourceType === "agent" && f.createdBy) sp.set("agent", f.createdBy);
  if (f.since) sp.set("since", f.since);
  if (f.until) sp.set("until", f.until);
  if (typeof f.relevanceMin === "number" && f.relevanceMin > 0) {
    sp.set("rel_min", String(f.relevanceMin));
  }
  if (f.includeExpired) sp.set("expired", "1");
  if (f.includeArchived) sp.set("archived", "1");
  if (f.orderBy && f.orderBy !== "created_at:desc") sp.set("sort", f.orderBy);
  return sp;
}

// ---------------------------------------------------------------------------
// MemoryBrowserPage
// ---------------------------------------------------------------------------

export function MemoryBrowserPage() {
  const { t } = useTranslation("misc");
  const { currentWorkspace } = useWorkspaceStore();
  const {
    memories,
    searchResults,
    total,
    loading,
    fetchMemories,
    searchMemories,
    createMemory,
    deleteMemory,
  } = useMemoryStore();
  const { agents, fetchAgents } = useAgentStore();

  const [searchParams, setSearchParams] = useSearchParams();
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [showFilters, setShowFilters] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const workspaceId = currentWorkspace?.id ?? "";

  // Filter state lives in the URL — every change is a shareable link.
  const filter = useMemo(() => filterFromParams(searchParams), [searchParams]);
  const activeCount = countActiveFilters(filter);

  const updateFilter = useCallback(
    (next: MemoryFilter) => {
      setSearchParams(paramsFromFilter(next), { replace: true });
    },
    [setSearchParams],
  );

  const resetFilters = useCallback(() => {
    // Preserve the free-text query; clear the rest.
    setSearchParams(paramsFromFilter({ ...emptyFilter, q: filter.q }), {
      replace: true,
    });
  }, [setSearchParams, filter.q]);

  // Load agents once for the "source agent" dropdown.
  useEffect(() => {
    if (workspaceId) fetchAgents(workspaceId);
  }, [workspaceId, fetchAgents]);

  // Debounced fetch whenever the workspace or any filter changes.
  useEffect(() => {
    if (!workspaceId) return;
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      const q = (filter.q ?? "").trim();
      if (q) {
        searchMemories(q, workspaceId, filter);
      } else {
        fetchMemories(workspaceId, filter);
      }
    }, 300);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [workspaceId, filter, fetchMemories, searchMemories]);

  const handleDelete = async (id: string) => {
    await deleteMemory(id);
  };

  const handleCreate = async (data: Partial<Memory>) => {
    await createMemory(data);
    setShowCreateForm(false);
  };

  const isSearching = (filter.q ?? "").trim().length > 0;
  const displayItems = isSearching ? searchResults : memories;
  const hasItems = displayItems.length > 0;

  // Distinct tags from the current result set — feeds the tag multi-select.
  const availableTags = useMemo(() => {
    const set = new Set<string>();
    for (const m of displayItems) for (const t of m.tags ?? []) set.add(t);
    return [...set].sort();
  }, [displayItems]);

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Action bar */}
      <div className="flex items-center justify-end border-b border-border px-6 py-4 shrink-0">
        <Button
          variant="outline"
          size="sm"
          onClick={() => setShowCreateForm((v) => !v)}
          className="gap-1.5"
        >
          <Plus className="h-3.5 w-3.5" />
          {t("memory.page.new")}
        </Button>
      </div>

      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl px-6 py-6 space-y-4">
          {/* Create form */}
          {showCreateForm && (
            <Card className="p-4">
              <h2 className="text-sm font-semibold mb-3">{t("memory.page.create")}</h2>
              <CreateMemoryForm
                onSubmit={handleCreate}
                workspaceId={workspaceId}
              />
            </Card>
          )}

          {/* Search + filter toggle bar */}
          <div className="flex gap-2">
            <div className="relative flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                placeholder={t("memory.page.searchPlaceholder")}
                value={filter.q ?? ""}
                onChange={(e) => updateFilter({ ...filter, q: e.target.value })}
                className="pl-8"
              />
            </div>
            <Button
              variant={showFilters ? "secondary" : "outline"}
              size="sm"
              onClick={() => setShowFilters((v) => !v)}
              className="gap-1.5 shrink-0"
            >
              <SlidersHorizontal className="h-3.5 w-3.5" />
              {t("memory.page.filters")}
              {activeCount > 0 && (
                <Badge variant="default" className="ml-0.5 px-1.5 py-0">
                  {activeCount}
                </Badge>
              )}
            </Button>
          </div>

          {/* Filter panel */}
          {showFilters && (
            <MemoryFiltersPanel
              filter={filter}
              onChange={updateFilter}
              onReset={resetFilters}
              agents={agents}
              availableTags={availableTags}
            />
          )}

          {/* Active-filter summary bar */}
          {activeCount > 0 && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span>
                {t("memory.page.activeFilters", { count: activeCount })}
              </span>
              <button
                onClick={resetFilters}
                className="inline-flex items-center gap-1 text-foreground hover:text-destructive transition-colors"
              >
                <X className="h-3 w-3" /> {t("memory.page.clear")}
              </button>
            </div>
          )}

          {/* Results info */}
          {!loading && (
            <p className="text-xs text-muted-foreground">
              {t("memory.page.results", { count: total })}
              {isSearching && (
                <>{t("memory.page.resultsFor", { query: (filter.q ?? "").trim() })}</>
              )}
            </p>
          )}

          {/* List */}
          {loading ? (
            <div className="space-y-3">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-28 w-full rounded-xl" />
              ))}
            </div>
          ) : hasItems ? (
            <div className="space-y-3">
              {displayItems.map((m) => (
                <MemoryCard
                  key={m.id}
                  memory={m}
                  onDelete={handleDelete}
                />
              ))}
            </div>
          ) : (
            <div className="flex flex-col items-center justify-center py-20 text-center">
              <Brain className="h-10 w-10 text-muted-foreground/40 mb-3" />
              <p className="text-sm font-medium text-muted-foreground">
                {isSearching || activeCount > 0
                  ? t("memory.page.noMatches")
                  : t("memory.page.empty")}
              </p>
              {(isSearching || activeCount > 0) && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="mt-3"
                  onClick={resetFilters}
                >
                  {t("memory.page.resetFilters")}
                </Button>
              )}
              {!isSearching && activeCount === 0 && (
                <p className="mt-1 text-xs text-muted-foreground/70">
                  {t("memory.page.emptyHint")}
                </p>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
