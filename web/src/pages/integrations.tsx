import { useCallback, useEffect, useState } from "react";
import { useParams } from "react-router";
import {
  CheckCircle2,
  Circle,
  Github,
  MessageSquare,
  Server,
  Sparkles,
} from "lucide-react";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/cn";
import type { IntegrationConfig, IntegrationProvider } from "@/types";

// ---------------------------------------------------------------------------
// Provider metadata
// ---------------------------------------------------------------------------

interface ProviderMeta {
  id: IntegrationProvider;
  name: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  comingSoon?: boolean;
}

const PROVIDERS: ProviderMeta[] = [
  {
    id: "github",
    name: "GitHub",
    description:
      "Link pull requests and commits to tasks. Receive webhook events to auto-update task status.",
    icon: Github,
  },
  {
    id: "slack",
    name: "Slack",
    description:
      "Send task updates and agent activity notifications to Slack channels.",
    icon: MessageSquare,
    comingSoon: true,
  },
  {
    id: "spark",
    name: "Spark Agent Catalog",
    description:
      "Browse and install AI agents from the Spark catalog into your workspace.",
    icon: Sparkles,
  },
  {
    id: "mcp",
    name: "MCP Server",
    description:
      "Connect AI agents (Claude Code, Cline, Aider) via Model Context Protocol. Supports stdio and SSE transports.",
    icon: Server,
  },
];

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function IntegrationsPage() {
  useParams();
  const { currentWorkspace } = useWorkspaceStore();

  const [configs, setConfigs] = useState<IntegrationConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [toggling, setToggling] = useState<string | null>(null);

  const fetchConfigs = useCallback(async () => {
    if (!currentWorkspace) return;
    try {
      const res = await api<{ integrations: IntegrationConfig[] }>(
        `/api/v1/workspaces/${currentWorkspace.id}/integrations`,
      );
      setConfigs(res.integrations ?? []);
    } catch {
      // ignore
    } finally {
      setLoading(false);
    }
  }, [currentWorkspace]);

  useEffect(() => {
    void fetchConfigs();
  }, [fetchConfigs]);

  const getConfig = (provider: IntegrationProvider) =>
    configs.find((c) => c.provider === provider);

  const handleToggle = async (provider: IntegrationProvider, enabled: boolean) => {
    if (!currentWorkspace) return;
    setToggling(provider);
    try {
      const existing = getConfig(provider);
      if (existing) {
        const updated = await api<IntegrationConfig>(
          `/api/v1/integrations/${existing.id}`,
          { method: "PATCH", body: { is_active: enabled } },
        );
        setConfigs((prev) =>
          prev.map((c) => (c.id === updated.id ? updated : c)),
        );
      } else {
        const created = await api<IntegrationConfig>(
          `/api/v1/workspaces/${currentWorkspace.id}/integrations`,
          {
            method: "POST",
            body: { provider, config: {}, is_active: enabled },
          },
        );
        setConfigs((prev) => [...prev, created]);
      }
    } catch {
      // ignore
    } finally {
      setToggling(null);
    }
  };

  const handleDelete = async (provider: IntegrationProvider) => {
    const existing = getConfig(provider);
    if (!existing) return;
    setToggling(provider);
    try {
      await api(`/api/v1/integrations/${existing.id}`, { method: "DELETE" });
      setConfigs((prev) => prev.filter((c) => c.id !== existing.id));
    } catch {
      // ignore
    } finally {
      setToggling(null);
    }
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 overflow-y-auto">
        <p className="mb-6 text-sm text-muted-foreground max-w-prose">
          Connect Entire VC Mesh with external services to automate workflows and
          keep your team in sync.
        </p>

        {loading ? (
          <div className="space-y-4">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-32 w-full rounded-xl" />
            ))}
          </div>
        ) : (
          <div className="space-y-4 max-w-2xl">
            {PROVIDERS.map((provider) => {
              const cfg = getConfig(provider.id);
              const isActive = cfg?.is_active ?? false;
              const isLoading = toggling === provider.id;
              const Icon = provider.icon;

              return (
                <Card key={provider.id}>
                  <CardHeader className="pb-3">
                    <div className="flex items-start justify-between">
                      <div className="flex items-center gap-3">
                        <div className="flex h-10 w-10 items-center justify-center rounded-lg border border-border bg-muted">
                          <Icon className="h-5 w-5" />
                        </div>
                        <div>
                          <CardTitle className="flex items-center gap-2 text-base">
                            {provider.name}
                            {provider.comingSoon && (
                              <Badge
                                variant="outline"
                                className="text-[10px] px-1.5 py-0"
                              >
                                Coming soon
                              </Badge>
                            )}
                          </CardTitle>
                          <p className="text-xs text-muted-foreground">
                            {provider.description}
                          </p>
                        </div>
                      </div>

                      <div className="flex items-center gap-2 shrink-0 ml-4">
                        {cfg && !provider.comingSoon && (
                          <button
                            onClick={() => void handleDelete(provider.id)}
                            className="text-xs text-muted-foreground hover:text-destructive transition-colors"
                            disabled={isLoading}
                          >
                            Remove
                          </button>
                        )}
                        <Button
                          size="sm"
                          variant={isActive ? "default" : "outline"}
                          disabled={isLoading || provider.comingSoon}
                          onClick={() =>
                            void handleToggle(provider.id, !isActive)
                          }
                          className={cn(
                            isActive &&
                              "bg-teal-600 hover:bg-teal-700 border-teal-600",
                          )}
                        >
                          {isLoading ? (
                            "..."
                          ) : isActive ? (
                            <span className="flex items-center gap-1.5">
                              <CheckCircle2 className="h-3.5 w-3.5" />
                              Enabled
                            </span>
                          ) : (
                            <span className="flex items-center gap-1.5">
                              <Circle className="h-3.5 w-3.5" />
                              Enable
                            </span>
                          )}
                        </Button>
                      </div>
                    </div>
                  </CardHeader>

                  {provider.id === "github" && isActive && (
                    <CardContent className="pt-0">
                      <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                        <p className="text-xs font-medium text-muted-foreground mb-1">
                          Webhook URL
                        </p>
                        <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
                          {window.location.origin}/webhooks/github
                        </code>
                        <p className="mt-2 text-xs text-muted-foreground">
                          Add this URL to your GitHub repository under{" "}
                          <strong>Settings &rarr; Webhooks</strong>. Select{" "}
                          <em>Pull requests</em> and <em>Pushes</em> events.
                          Include{" "}
                          <code className="rounded bg-background px-1">
                            MESH-{"<task-id>"}
                          </code>{" "}
                          in commit messages or PR titles to auto-link tasks.
                        </p>
                      </div>
                    </CardContent>
                  )}

                  {provider.id === "slack" && provider.comingSoon && (
                    <CardContent className="pt-0">
                      <p className="text-xs text-muted-foreground">
                        Slack integration is planned for a future release.
                        Configure notification channels and event triggers.
                      </p>
                    </CardContent>
                  )}

                  {provider.id === "spark" && isActive && (
                    <CardContent className="pt-0">
                      <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                        <p className="text-xs text-muted-foreground">
                          Spark catalog is enabled. Go to{" "}
                          <a
                            href={`/w/${currentWorkspace?.slug}/spark`}
                            className="text-primary hover:underline font-medium"
                          >
                            Spark Catalog
                          </a>{" "}
                          to browse and install agents.
                        </p>
                      </div>
                    </CardContent>
                  )}

                  {provider.id === "mcp" && isActive && (
                    <CardContent className="pt-0">
                      <div className="space-y-3">
                        <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                          <p className="text-xs font-medium text-muted-foreground mb-1">
                            SSE Endpoint
                          </p>
                          <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
                            {window.location.origin}/mcp/sse
                          </code>
                        </div>

                        <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                          <p className="text-xs font-medium text-muted-foreground mb-1">
                            Claude Code / Cline — .mcp.json
                          </p>
                          <pre className="rounded bg-background px-2 py-1.5 text-xs font-mono overflow-x-auto select-all whitespace-pre">{`{
  "mcpServers": {
    "evc-mesh": {
      "command": "mesh-mcp",
      "env": {
        "MESH_API_URL": "${window.location.origin}",
        "MESH_AGENT_KEY": "agk_<your_agent_key>"
      }
    }
  }
}`}</pre>
                        </div>

                        <p className="text-xs text-muted-foreground">
                          Register an agent in{" "}
                          <a
                            href={`/w/${currentWorkspace?.slug}/agents`}
                            className="text-primary hover:underline font-medium"
                          >
                            Agents
                          </a>{" "}
                          to get an API key, then use it in{" "}
                          <code className="rounded bg-muted px-1">MESH_AGENT_KEY</code>.
                          MCP provides 25 tools: task management, comments, events, artifacts, and more.
                        </p>
                      </div>
                    </CardContent>
                  )}
                </Card>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
