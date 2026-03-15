import { useCallback, useEffect, useRef, useState } from "react";
import { Brain, ChevronDown, ChevronUp, Plus, Search, Trash2, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemoryStore } from "@/stores/memory";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { MarkdownRenderer } from "@/components/markdown-renderer";
import type { Memory } from "@/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function scopeBadgeClass(scope: Memory["scope"]): string {
  switch (scope) {
    case "workspace":
      return "bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300";
    case "project":
      return "bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300";
    case "agent":
      return "bg-purple-100 text-purple-700 dark:bg-purple-900 dark:text-purple-300";
  }
}

function sourceLabel(source: Memory["source_type"]): string {
  switch (source) {
    case "agent":
      return "Agent";
    case "human":
      return "Human";
    case "system":
      return "System";
  }
}

function relevanceBarClass(relevance: number): string {
  if (relevance >= 0.7) return "bg-green-500";
  if (relevance >= 0.3) return "bg-yellow-500";
  return "bg-red-400";
}

// ---------------------------------------------------------------------------
// MemoryCard
// ---------------------------------------------------------------------------

interface MemoryCardProps {
  memory: Memory & { score?: number };
  onDelete: (id: string) => void;
}

function MemoryCard({ memory, onDelete }: MemoryCardProps) {
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
              {memory.scope}
            </span>
            {memory.score !== undefined && (
              <span className="inline-flex items-center rounded-md bg-muted px-2 py-0.5 text-xs text-muted-foreground">
                score {memory.score.toFixed(3)}
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
            <MarkdownRenderer content={memory.content} />
          </div>

          {/* Expand / collapse toggle */}
          <button
            onClick={() => setExpanded((v) => !v)}
            className="mt-1 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
          >
            {expanded ? (
              <>
                <ChevronUp className="h-3 w-3" /> Show less
              </>
            ) : (
              <>
                <ChevronDown className="h-3 w-3" /> Show more
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
              <span className="text-xs text-muted-foreground">Relevance</span>
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
              {sourceLabel(memory.source_type)} &middot; updated{" "}
              {formatRelative(memory.updated_at)}
            </span>
            {isStale && (
              <Badge variant="warning" className="text-xs">
                stale
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
                Confirm
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
      setError(err instanceof Error ? err.message : "Failed to create memory");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label className="text-xs font-medium text-muted-foreground mb-1 block">
            Key (slug format)
          </label>
          <Input
            placeholder="e.g. project-context"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            pattern="[a-z0-9_\-]+"
            title="Lowercase letters, numbers, hyphens and underscores only"
            required
          />
        </div>
        <div>
          <label className="text-xs font-medium text-muted-foreground mb-1 block">
            Scope
          </label>
          <Select
            value={scope}
            onChange={(e) => setScope(e.target.value as Memory["scope"])}
          >
            <option value="workspace">Workspace</option>
            <option value="project">Project</option>
            <option value="agent">Agent</option>
          </Select>
        </div>
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground mb-1 block">
          Content
        </label>
        <Textarea
          placeholder="Memory content (Markdown supported)"
          value={content}
          onChange={(e) => setContent(e.target.value)}
          className="min-h-[80px]"
          required
        />
      </div>

      <div>
        <label className="text-xs font-medium text-muted-foreground mb-1 block">
          Tags (comma-separated)
        </label>
        <Input
          placeholder="e.g. context, onboarding, api"
          value={tagsRaw}
          onChange={(e) => setTagsRaw(e.target.value)}
        />
      </div>

      {error && <p className="text-xs text-destructive">{error}</p>}

      <div className="flex justify-end">
        <Button type="submit" disabled={submitting || !key.trim() || !content.trim()}>
          {submitting ? "Saving..." : "Save Memory"}
        </Button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// MemoryBrowserPage
// ---------------------------------------------------------------------------

export function MemoryBrowserPage() {
  const { currentWorkspace } = useWorkspaceStore();
  const {
    memories,
    searchResults,
    loading,
    searchQuery,
    fetchMemories,
    searchMemories,
    createMemory,
    deleteMemory,
    setSearchQuery,
  } = useMemoryStore();

  const [scope, setScope] = useState("all");
  const [showCreateForm, setShowCreateForm] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const workspaceId = currentWorkspace?.id ?? "";

  // Initial load
  useEffect(() => {
    if (!workspaceId) return;
    fetchMemories(workspaceId, scope);
  }, [workspaceId, scope, fetchMemories]);

  // Debounced search
  const handleSearchChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const q = e.target.value;
      setSearchQuery(q);
      if (debounceRef.current) clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(() => {
        if (!workspaceId) return;
        if (q.trim()) {
          searchMemories(q.trim(), workspaceId, scope);
        } else {
          fetchMemories(workspaceId, scope);
        }
      }, 300);
    },
    [workspaceId, scope, fetchMemories, searchMemories, setSearchQuery],
  );

  const handleScopeChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const newScope = e.target.value;
    setScope(newScope);
    // scope change triggers useEffect above
  };

  const handleDelete = async (id: string) => {
    await deleteMemory(id);
  };

  const handleCreate = async (data: Partial<Memory>) => {
    await createMemory(data);
    setShowCreateForm(false);
  };

  const displayItems = searchQuery.trim() ? searchResults : memories;
  const hasItems = displayItems.length > 0;

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Page header */}
      <div className="flex items-center justify-between border-b border-border px-6 py-4 shrink-0">
        <div className="flex items-center gap-2">
          <Brain className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-lg font-semibold">Memory Browser</h1>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setShowCreateForm((v) => !v)}
          className="gap-1.5"
        >
          <Plus className="h-3.5 w-3.5" />
          New Memory
        </Button>
      </div>

      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl px-6 py-6 space-y-4">
          {/* Create form */}
          {showCreateForm && (
            <Card className="p-4">
              <h2 className="text-sm font-semibold mb-3">Create Memory</h2>
              <CreateMemoryForm
                onSubmit={handleCreate}
                workspaceId={workspaceId}
              />
            </Card>
          )}

          {/* Search + filter bar */}
          <div className="flex gap-2">
            <div className="relative flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                placeholder="Search memories..."
                value={searchQuery}
                onChange={handleSearchChange}
                className="pl-8"
              />
            </div>
            <Select value={scope} onChange={handleScopeChange} className="w-36">
              <option value="all">All scopes</option>
              <option value="workspace">Workspace</option>
              <option value="project">Project</option>
              <option value="agent">Agent</option>
            </Select>
          </div>

          {/* Results info */}
          {searchQuery.trim() && !loading && (
            <p className="text-xs text-muted-foreground">
              {searchResults.length} result{searchResults.length !== 1 ? "s" : ""} for &ldquo;{searchQuery}&rdquo;
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
                {searchQuery.trim()
                  ? "No memories matched your search."
                  : "No memories yet."}
              </p>
              {!searchQuery.trim() && (
                <p className="mt-1 text-xs text-muted-foreground/70">
                  Use the recall/remember MCP tools or create one above.
                </p>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
