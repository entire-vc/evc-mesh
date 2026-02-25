import { useCallback, useEffect, useState } from "react";
import { Download, Search, Sparkles, Star, Tag, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { useWorkspaceStore } from "@/stores/workspace";
import { useSparkStore } from "@/stores/spark";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import type { SparkAgentManifest } from "@/types";

// Maps agent_type string to display label and color.
function agentTypeLabel(agentType: string): { label: string; color: string } {
  const map: Record<string, { label: string; color: string }> = {
    claude_code: { label: "Claude Code", color: "bg-purple-100 text-purple-700" },
    openclaw: { label: "OpenClaw", color: "bg-blue-100 text-blue-700" },
    cline: { label: "Cline", color: "bg-green-100 text-green-700" },
    aider: { label: "Aider", color: "bg-orange-100 text-orange-700" },
    custom: { label: "Custom", color: "bg-gray-100 text-gray-700" },
  };
  return map[agentType] ?? { label: agentType, color: "bg-gray-100 text-gray-700" };
}

export function SparkPage() {
  const { currentWorkspace } = useWorkspaceStore();
  const {
    agents,
    popularAgents,
    isLoading,
    error,
    search,
    fetchPopular,
    selectAgent,
    selectedAgent,
    clearError,
  } = useSparkStore();

  const [query, setQuery] = useState("");
  const [tagInput, setTagInput] = useState("");
  const [activeTags, setActiveTags] = useState<string[]>([]);
  const [detailOpen, setDetailOpen] = useState(false);
  const [installOpen, setInstallOpen] = useState(false);
  const [hasSearched, setHasSearched] = useState(false);

  // Load popular agents on mount.
  useEffect(() => {
    fetchPopular(20);
  }, [fetchPopular]);

  const handleSearch = useCallback(async () => {
    setHasSearched(true);
    await search(query, activeTags, 20);
  }, [query, activeTags, search]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (e.key === "Enter") {
        void handleSearch();
      }
    },
    [handleSearch],
  );

  const addTag = useCallback(() => {
    const tag = tagInput.trim();
    if (tag && !activeTags.includes(tag)) {
      setActiveTags((prev) => [...prev, tag]);
    }
    setTagInput("");
  }, [tagInput, activeTags]);

  const removeTag = useCallback((tag: string) => {
    setActiveTags((prev) => prev.filter((t) => t !== tag));
  }, []);

  const handleCardClick = useCallback(
    (agent: SparkAgentManifest) => {
      selectAgent(agent);
      setDetailOpen(true);
    },
    [selectAgent],
  );

  const handleInstallClick = useCallback(
    (agent: SparkAgentManifest) => {
      selectAgent(agent);
      setDetailOpen(false);
      setInstallOpen(true);
    },
    [selectAgent],
  );

  const displayedAgents = hasSearched ? agents : popularAgents;
  const sectionTitle = hasSearched ? "Search results" : "Popular agents";

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center gap-3">
        <Sparkles className="h-6 w-6 text-muted-foreground" />
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Spark Catalog</h1>
          <p className="text-muted-foreground">
            Browse and install AI agents from the Spark catalog
          </p>
        </div>
      </div>

      {/* Error banner */}
      {error && (
        <div className="flex items-center justify-between rounded-lg bg-destructive/10 px-4 py-3 text-sm text-destructive">
          <span>{error}</span>
          <button onClick={clearError} className="ml-2 hover:opacity-70">
            <X className="h-4 w-4" />
          </button>
        </div>
      )}

      {/* Search bar */}
      <div className="space-y-3">
        <div className="flex gap-2">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              placeholder="Search agents..."
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={handleKeyDown}
              className="pl-9"
            />
          </div>
          <Button onClick={() => void handleSearch()}>Search</Button>
        </div>

        {/* Tag filter */}
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex gap-2">
            <div className="relative">
              <Tag className="absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                placeholder="Add tag..."
                value={tagInput}
                onChange={(e) => setTagInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    addTag();
                  }
                }}
                className="h-8 w-32 pl-8 text-xs"
              />
            </div>
            <Button variant="outline" size="sm" onClick={addTag} className="h-8 text-xs">
              Add tag
            </Button>
          </div>
          {activeTags.map((tag) => (
            <Badge
              key={tag}
              variant="secondary"
              className="cursor-pointer gap-1 text-xs"
              onClick={() => removeTag(tag)}
            >
              {tag}
              <X className="h-3 w-3" />
            </Badge>
          ))}
        </div>
      </div>

      {/* Results */}
      <div>
        <h2 className="mb-3 text-sm font-semibold text-muted-foreground uppercase tracking-wider">
          {sectionTitle}
        </h2>

        {isLoading ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {Array.from({ length: 6 }).map((_, i) => (
              <Card key={i}>
                <CardHeader>
                  <Skeleton className="h-5 w-32" />
                  <Skeleton className="h-4 w-20" />
                </CardHeader>
                <CardContent>
                  <Skeleton className="h-4 w-full" />
                  <Skeleton className="mt-2 h-4 w-3/4" />
                </CardContent>
              </Card>
            ))}
          </div>
        ) : displayedAgents.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center py-12">
              <Sparkles className="mb-4 h-12 w-12 text-muted-foreground" />
              {hasSearched ? (
                <>
                  <h3 className="mb-2 text-lg font-semibold">No agents found</h3>
                  <p className="text-center text-sm text-muted-foreground">
                    Try a different search query or remove some tags.
                  </p>
                </>
              ) : (
                <>
                  <h3 className="mb-2 text-lg font-semibold">
                    Spark catalog unavailable
                  </h3>
                  <p className="text-center text-sm text-muted-foreground">
                    The Spark catalog could not be reached. Check that
                    MESH_SPARK_ENABLED=true and the service is running.
                  </p>
                </>
              )}
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {displayedAgents.map((agent) => (
              <SparkAgentCard
                key={agent.id}
                agent={agent}
                onView={() => handleCardClick(agent)}
                onInstall={() => handleInstallClick(agent)}
              />
            ))}
          </div>
        )}
      </div>

      {/* Detail drawer/modal */}
      {selectedAgent && (
        <AgentDetailDialog
          open={detailOpen}
          onOpenChange={setDetailOpen}
          agent={selectedAgent}
          onInstall={() => handleInstallClick(selectedAgent)}
        />
      )}

      {/* Install dialog */}
      {selectedAgent && currentWorkspace && (
        <InstallDialog
          open={installOpen}
          onOpenChange={setInstallOpen}
          agent={selectedAgent}
          workspaceId={currentWorkspace.id}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Agent Card
// ---------------------------------------------------------------------------

function SparkAgentCard({
  agent,
  onView,
  onInstall,
}: {
  agent: SparkAgentManifest;
  onView: () => void;
  onInstall: () => void;
}) {
  const typeConfig = agentTypeLabel(agent.agent_type);

  return (
    <Card
      className="flex flex-col cursor-pointer transition-shadow hover:shadow-md"
      onClick={onView}
    >
      <CardHeader className="pb-2">
        <div className="flex items-start justify-between gap-2">
          <CardTitle className="text-base leading-tight">{agent.name}</CardTitle>
          <Badge className={cn("shrink-0 text-xs", typeConfig.color)}>
            {typeConfig.label}
          </Badge>
        </div>
        <p className="text-xs text-muted-foreground">by {agent.author}</p>
      </CardHeader>
      <CardContent className="flex flex-1 flex-col gap-3">
        <p className="line-clamp-2 text-sm text-muted-foreground">
          {agent.description || "No description provided."}
        </p>

        {/* Tags */}
        {agent.tags && agent.tags.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {agent.tags.slice(0, 4).map((tag) => (
              <Badge key={tag} variant="outline" className="text-[10px]">
                {tag}
              </Badge>
            ))}
            {agent.tags.length > 4 && (
              <Badge variant="outline" className="text-[10px]">
                +{agent.tags.length - 4}
              </Badge>
            )}
          </div>
        )}

        {/* Stats row */}
        <div className="mt-auto flex items-center justify-between text-xs text-muted-foreground">
          <div className="flex items-center gap-3">
            <span className="flex items-center gap-1">
              <Download className="h-3 w-3" />
              {agent.downloads.toLocaleString()}
            </span>
            <span className="flex items-center gap-1">
              <Star className="h-3 w-3" />
              {agent.rating.toFixed(1)}
            </span>
          </div>
          <span className="text-[10px]">v{agent.version}</span>
        </div>

        <Button
          size="sm"
          className="w-full"
          onClick={(e) => {
            e.stopPropagation();
            onInstall();
          }}
        >
          Install
        </Button>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Agent Detail Dialog
// ---------------------------------------------------------------------------

function AgentDetailDialog({
  open,
  onOpenChange,
  agent,
  onInstall,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agent: SparkAgentManifest;
  onInstall: () => void;
}) {
  const typeConfig = agentTypeLabel(agent.agent_type);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        onClose={() => onOpenChange(false)}
        className="max-w-lg max-h-[80vh] overflow-y-auto"
      >
        <DialogHeader>
          <div className="flex items-start justify-between gap-2">
            <DialogTitle>{agent.name}</DialogTitle>
            <Badge className={cn("shrink-0 text-xs", typeConfig.color)}>
              {typeConfig.label}
            </Badge>
          </div>
          <DialogDescription>
            by {agent.author} &middot; v{agent.version}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* Description */}
          <p className="text-sm text-muted-foreground">
            {agent.description || "No description provided."}
          </p>

          {/* Stats */}
          <div className="flex gap-4 text-sm">
            <span className="flex items-center gap-1.5">
              <Download className="h-4 w-4 text-muted-foreground" />
              <strong>{agent.downloads.toLocaleString()}</strong> downloads
            </span>
            <span className="flex items-center gap-1.5">
              <Star className="h-4 w-4 text-muted-foreground" />
              <strong>{agent.rating.toFixed(1)}</strong> rating
            </span>
          </div>

          {/* Tags */}
          {agent.tags && agent.tags.length > 0 && (
            <div className="space-y-1">
              <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Tags
              </p>
              <div className="flex flex-wrap gap-1">
                {agent.tags.map((tag) => (
                  <Badge key={tag} variant="outline" className="text-xs">
                    {tag}
                  </Badge>
                ))}
              </div>
            </div>
          )}

          {/* Capabilities */}
          {agent.capabilities && Object.keys(agent.capabilities ?? {}).length > 0 && (
            <div className="space-y-1">
              <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Capabilities
              </p>
              <pre className="overflow-auto rounded bg-muted p-3 text-xs">
                {JSON.stringify(agent.capabilities, null, 2)}
              </pre>
            </div>
          )}

          {/* Config template */}
          {agent.config && Object.keys(agent.config ?? {}).length > 0 && (
            <div className="space-y-1">
              <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Config template
              </p>
              <pre className="overflow-auto rounded bg-muted p-3 text-xs">
                {JSON.stringify(agent.config, null, 2)}
              </pre>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
          <Button onClick={onInstall}>Install in workspace</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Install Dialog
// ---------------------------------------------------------------------------

function InstallDialog({
  open,
  onOpenChange,
  agent,
  workspaceId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agent: SparkAgentManifest;
  workspaceId: string;
}) {
  const { install, isInstalling, lastInstallResult, clearInstallResult, error } =
    useSparkStore();
  const [done, setDone] = useState(false);
  const [copied, setCopied] = useState(false);
  const [installError, setInstallError] = useState<string | null>(null);

  const handleInstall = useCallback(async () => {
    setInstallError(null);
    try {
      await install(agent.id, workspaceId);
      setDone(true);
    } catch (err) {
      setInstallError(err instanceof Error ? err.message : "Installation failed");
    }
  }, [agent.id, workspaceId, install]);

  const handleCopyKey = useCallback(async () => {
    if (lastInstallResult?.api_key) {
      await navigator.clipboard.writeText(lastInstallResult.api_key);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [lastInstallResult?.api_key]);

  const handleClose = useCallback(() => {
    clearInstallResult();
    setDone(false);
    setCopied(false);
    setInstallError(null);
    onOpenChange(false);
  }, [clearInstallResult, onOpenChange]);

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent onClose={handleClose}>
        <DialogHeader>
          <DialogTitle>
            {done ? "Agent installed" : `Install "${agent.name}"`}
          </DialogTitle>
          <DialogDescription>
            {done
              ? "Save your API key — it will only be shown once."
              : `This will register "${agent.name}" as an agent in your workspace.`}
          </DialogDescription>
        </DialogHeader>

        {done && lastInstallResult ? (
          <div className="space-y-4 py-2">
            <div className="rounded-lg border border-border bg-muted/50 p-4">
              <p className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
                API Key (copy now — shown once)
              </p>
              <code className="break-all text-xs">{lastInstallResult.api_key}</code>
            </div>
            <Button variant="outline" className="w-full" onClick={() => void handleCopyKey()}>
              {copied ? "Copied!" : "Copy API key"}
            </Button>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <p className="text-sm text-muted-foreground">
              Agent will be created with type{" "}
              <strong>{agent.agent_type}</strong> and capabilities from the
              Spark manifest.
            </p>
            {(installError ?? error) && (
              <div className="rounded-lg bg-destructive/10 p-3 text-sm text-destructive">
                {installError ?? error}
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          {done ? (
            <Button onClick={handleClose}>Done</Button>
          ) : (
            <>
              <Button variant="outline" onClick={handleClose}>
                Cancel
              </Button>
              <Button onClick={() => void handleInstall()} disabled={isInstalling}>
                {isInstalling ? "Installing..." : "Install"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
