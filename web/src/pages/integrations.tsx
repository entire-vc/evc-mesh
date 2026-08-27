import { useCallback, useEffect, useState } from "react";
import { useParams } from "react-router";
import {
  CheckCircle2,
  Circle,
  GitBranch,
  GitMerge,
  MessageSquare,
  Send,
  Server,
  Sparkles,
} from "lucide-react";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { apiErrorMessage } from "@/lib/api-error";
import type {
  GitHubIntegrationConfig,
  GitLabIntegrationConfig,
  IntegrationConfig,
  IntegrationProvider,
  TelegramIntegrationConfig,
} from "@/types";

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
    icon: GitBranch,
  },
  {
    id: "gitlab",
    name: "GitLab",
    description:
      "Link merge requests to tasks on a self-hosted GitLab instance. Receive webhook events to auto-update task status.",
    icon: GitMerge,
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
  {
    id: "telegram",
    name: "Telegram",
    description:
      "Deliver notifications and activity to a Telegram bot. Each member connects their own account from Notification Settings.",
    icon: Send,
  },
];

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function IntegrationsPage() {
  useParams();
  const { currentWorkspace } = useWorkspaceStore();
  const { myRole, fetchMyRole } = useMemberStore();

  const [configs, setConfigs] = useState<IntegrationConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [toggling, setToggling] = useState<string | null>(null);

  const [telegramTokenInput, setTelegramTokenInput] = useState("");
  const [telegramEditingToken, setTelegramEditingToken] = useState(false);
  const [telegramSaving, setTelegramSaving] = useState(false);

  // GitHub/GitLab connection form state — one editing flag + field values per
  // provider, keyed by provider id. Mirrors the Telegram token form above;
  // token_set/webhook_secret_set (from the masked API response) decide
  // whether "Connected" or the raw form renders, the same way
  // telegramTokenSet does for Telegram.
  const [vcsEditing, setVcsEditing] = useState<Record<string, boolean>>({});
  const [vcsFormValues, setVcsFormValues] = useState<
    Record<string, { base_url: string; token: string; webhook_secret: string }>
  >({});
  const [vcsSaving, setVcsSaving] = useState<string | null>(null);

  const canManageTelegram = myRole === "owner" || myRole === "admin";

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

  useEffect(() => {
    if (currentWorkspace) void fetchMyRole(currentWorkspace.id);
  }, [currentWorkspace, fetchMyRole]);

  const getConfig = (provider: IntegrationProvider) =>
    configs.find((c) => c.provider === provider);

  const handleSaveTelegramToken = async () => {
    if (!currentWorkspace) return;
    const token = telegramTokenInput.trim();
    if (!token) {
      toast.error("Enter a bot token");
      return;
    }

    setTelegramSaving(true);
    try {
      const existing = getConfig("telegram");
      const updated = existing
        ? await api<IntegrationConfig>(`/api/v1/integrations/${existing.id}`, {
            method: "PATCH",
            body: { config: { bot_token: token }, is_active: true },
          })
        : await api<IntegrationConfig>(
            `/api/v1/workspaces/${currentWorkspace.id}/integrations`,
            {
              method: "POST",
              body: { provider: "telegram", config: { bot_token: token }, is_active: true },
            },
          );

      setConfigs((prev) =>
        existing ? prev.map((c) => (c.id === updated.id ? updated : c)) : [...prev, updated],
      );
      setTelegramTokenInput("");
      setTelegramEditingToken(false);
      const botUsername = (updated.config as TelegramIntegrationConfig).bot_username;
      toast.success(botUsername ? `Connected as @${botUsername}` : "Telegram bot connected");
    } catch (err) {
      toast.error(apiErrorMessage(err, "Failed to save the Telegram bot token"));
    } finally {
      setTelegramSaving(false);
    }
  };

  // handleSaveVcsConfig saves a GitHub/GitLab connection. Only non-empty
  // fields are sent — Configure/Update replace the whole config JSON blob
  // server-side, but the handler merges a partial submission onto whatever
  // is already stored (rotating just the webhook_secret does not wipe an
  // already-configured token), so leaving a field blank here means "keep
  // what's already saved", not "clear it".
  const handleSaveVcsConfig = async (provider: "github" | "gitlab") => {
    if (!currentWorkspace) return;
    const values = vcsFormValues[provider] ?? { base_url: "", token: "", webhook_secret: "" };
    const config: Record<string, string> = {};
    if (provider === "gitlab" && values.base_url.trim()) config.base_url = values.base_url.trim();
    if (values.token.trim()) config.token = values.token.trim();
    if (values.webhook_secret.trim()) config.webhook_secret = values.webhook_secret.trim();
    if (Object.keys(config).length === 0) {
      toast.error("Enter at least one field to save");
      return;
    }

    const label = provider === "github" ? "GitHub" : "GitLab";
    setVcsSaving(provider);
    try {
      const existing = getConfig(provider);
      const updated = existing
        ? await api<IntegrationConfig>(`/api/v1/integrations/${existing.id}`, {
            method: "PATCH",
            body: { config, is_active: true },
          })
        : await api<IntegrationConfig>(
            `/api/v1/workspaces/${currentWorkspace.id}/integrations`,
            { method: "POST", body: { provider, config, is_active: true } },
          );

      setConfigs((prev) =>
        existing ? prev.map((c) => (c.id === updated.id ? updated : c)) : [...prev, updated],
      );
      setVcsFormValues((prev) => ({ ...prev, [provider]: { base_url: "", token: "", webhook_secret: "" } }));
      setVcsEditing((prev) => ({ ...prev, [provider]: false }));
      toast.success(`${label} connection saved`);
    } catch (err) {
      toast.error(apiErrorMessage(err, `Failed to save the ${label} connection`));
    } finally {
      setVcsSaving(null);
    }
  };

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
            {PROVIDERS.filter(
              (provider) => provider.id !== "telegram" || canManageTelegram,
            ).map((provider) => {
              const cfg = getConfig(provider.id);
              const isActive = cfg?.is_active ?? false;
              const isLoading = toggling === provider.id;
              const Icon = provider.icon;
              const telegramCfg =
                provider.id === "telegram"
                  ? ((cfg?.config as TelegramIntegrationConfig | undefined) ?? {})
                  : undefined;
              const telegramTokenSet = telegramCfg?.bot_token_set === true;
              const githubCfg =
                provider.id === "github"
                  ? ((cfg?.config as GitHubIntegrationConfig | undefined) ?? {})
                  : undefined;
              const gitlabCfg =
                provider.id === "gitlab"
                  ? ((cfg?.config as GitLabIntegrationConfig | undefined) ?? {})
                  : undefined;
              const vcsValues =
                vcsFormValues[provider.id] ?? { base_url: "", token: "", webhook_secret: "" };

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
                        {cfg && !provider.comingSoon && provider.id !== "mcp" && (
                          <button
                            onClick={() => void handleDelete(provider.id)}
                            className="text-xs text-muted-foreground hover:text-destructive transition-colors"
                            disabled={isLoading}
                          >
                            Remove
                          </button>
                        )}
                        {/* mcp is a reference-only connection card — a static
                            .mcp.json snippet, not a channel. No enable/disable:
                            nothing on the backend reads is_active for it
                            (#4a3195a5), so a toggle here would control nothing. */}
                        {/* Telegram has nothing to toggle until a bot token has
                            been saved — the button here would just enable an
                            integration that can never poll anything. */}
                        {provider.id !== "mcp" &&
                        (provider.id !== "telegram" || telegramTokenSet) ? (
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
                        ) : null}
                      </div>
                    </div>
                  </CardHeader>

                  {provider.id === "github" && (
                    <CardContent className="pt-0 space-y-3">
                      <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                        <p className="text-xs font-medium text-muted-foreground mb-1">
                          Connection
                        </p>
                        {githubCfg?.token_set || githubCfg?.webhook_secret_set ? (
                          !vcsEditing.github ? (
                            <div className="flex items-center justify-between">
                              <p className="text-xs text-muted-foreground">
                                Using a workspace-configured token
                                {githubCfg.webhook_secret_set ? " and webhook secret" : ""}.
                              </p>
                              <button
                                onClick={() =>
                                  setVcsEditing((prev) => ({ ...prev, github: true }))
                                }
                                className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
                              >
                                Change
                              </button>
                            </div>
                          ) : (
                            <VcsConnectForm
                              provider="github"
                              values={vcsValues}
                              saving={vcsSaving === "github"}
                              onChange={(next) =>
                                setVcsFormValues((prev) => ({ ...prev, github: next }))
                              }
                              onSave={() => void handleSaveVcsConfig("github")}
                              onCancel={() =>
                                setVcsEditing((prev) => ({ ...prev, github: false }))
                              }
                              showCancel
                            />
                          )
                        ) : (
                          <>
                            <p className="text-xs text-muted-foreground mb-2">
                              Optional: connect a personal access token for live PR-status
                              checks and to set a workspace-specific webhook secret. Without
                              one, this deployment's instance-wide default is used.
                            </p>
                            <VcsConnectForm
                              provider="github"
                              values={vcsValues}
                              saving={vcsSaving === "github"}
                              onChange={(next) =>
                                setVcsFormValues((prev) => ({ ...prev, github: next }))
                              }
                              onSave={() => void handleSaveVcsConfig("github")}
                            />
                          </>
                        )}
                      </div>

                      {isActive && (
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
                      )}
                    </CardContent>
                  )}

                  {provider.id === "gitlab" && (
                    <CardContent className="pt-0 space-y-3">
                      <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                        <p className="text-xs font-medium text-muted-foreground mb-1">
                          Connection
                        </p>
                        {gitlabCfg?.token_set || gitlabCfg?.webhook_secret_set || gitlabCfg?.base_url ? (
                          !vcsEditing.gitlab ? (
                            <div className="flex items-center justify-between">
                              <p className="text-xs text-muted-foreground">
                                {gitlabCfg.base_url ? (
                                  <>
                                    Connected to{" "}
                                    <span className="font-medium text-foreground">
                                      {gitlabCfg.base_url}
                                    </span>
                                    .
                                  </>
                                ) : (
                                  "Connected."
                                )}
                              </p>
                              <button
                                onClick={() =>
                                  setVcsEditing((prev) => ({ ...prev, gitlab: true }))
                                }
                                className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
                              >
                                Change
                              </button>
                            </div>
                          ) : (
                            <VcsConnectForm
                              provider="gitlab"
                              values={vcsValues}
                              saving={vcsSaving === "gitlab"}
                              onChange={(next) =>
                                setVcsFormValues((prev) => ({ ...prev, gitlab: next }))
                              }
                              onSave={() => void handleSaveVcsConfig("gitlab")}
                              onCancel={() =>
                                setVcsEditing((prev) => ({ ...prev, gitlab: false }))
                              }
                              showCancel
                            />
                          )
                        ) : (
                          <>
                            <p className="text-xs text-muted-foreground mb-2">
                              Connect your self-hosted GitLab instance: base URL, an access
                              token for live MR-status checks, and a webhook secret.
                            </p>
                            <VcsConnectForm
                              provider="gitlab"
                              values={vcsValues}
                              saving={vcsSaving === "gitlab"}
                              onChange={(next) =>
                                setVcsFormValues((prev) => ({ ...prev, gitlab: next }))
                              }
                              onSave={() => void handleSaveVcsConfig("gitlab")}
                            />
                          </>
                        )}
                      </div>

                      {isActive && (
                        <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                          <p className="text-xs font-medium text-muted-foreground mb-1">
                            Webhook URL
                          </p>
                          <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
                            {window.location.origin}/webhooks/gitlab
                          </code>
                          <p className="mt-2 text-xs text-muted-foreground">
                            Add this URL to your GitLab project under{" "}
                            <strong>Settings &rarr; Webhooks</strong>. Select{" "}
                            <em>Merge request events</em>. Include{" "}
                            <code className="rounded bg-background px-1">
                              MESH-{"<task-id>"}
                            </code>{" "}
                            in commit messages or MR titles to auto-link tasks.
                          </p>
                        </div>
                      )}
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

                  {/* Always shown, not gated on is_active — mcp has no
                      enable/disable state (see the toggle-suppression above);
                      this is reference material, not something a workspace
                      turns on. */}
                  {provider.id === "mcp" && (
                    <CardContent className="pt-0">
                      <div className="space-y-3">
                        <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                          <p className="text-xs font-medium text-muted-foreground mb-1">
                            SSE Endpoint (remote agents)
                          </p>
                          <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
                            {window.location.origin}/mcp/sse
                          </code>
                          <p className="text-xs text-muted-foreground mt-1.5">
                            Requires your deployment to route <code className="rounded bg-muted px-1">/mcp</code> to the MCP server. If it does not connect, use the stdio config below.
                          </p>
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
                          MCP provides 34 tools: task management, comments, events, artifacts, and more.
                        </p>
                      </div>
                    </CardContent>
                  )}

                  {provider.id === "telegram" && (
                    <CardContent className="pt-0">
                      {telegramTokenSet && !telegramEditingToken ? (
                        <div className="flex items-center justify-between rounded-lg border border-dashed border-border bg-muted/30 p-3">
                          <p className="text-xs text-muted-foreground">
                            Connected as{" "}
                            <span className="font-medium text-foreground">
                              @{telegramCfg?.bot_username}
                            </span>
                            . Members connect from their own Notification Settings.
                          </p>
                          <button
                            onClick={() => setTelegramEditingToken(true)}
                            className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
                          >
                            Change token
                          </button>
                        </div>
                      ) : (
                        <div className="space-y-2">
                          <p className="text-xs text-muted-foreground">
                            Create a bot with{" "}
                            <span className="font-medium">@BotFather</span> on
                            Telegram and paste its token here. The token is
                            encrypted at rest and never shown again.
                          </p>
                          <div className="flex gap-2">
                            <Input
                              type="password"
                              placeholder="123456789:AAExampleBotTokenHere"
                              value={telegramTokenInput}
                              onChange={(e) => setTelegramTokenInput(e.target.value)}
                              disabled={telegramSaving}
                              className="font-mono text-xs"
                            />
                            <Button
                              size="sm"
                              onClick={() => void handleSaveTelegramToken()}
                              disabled={telegramSaving || !telegramTokenInput.trim()}
                            >
                              {telegramSaving ? "Connecting…" : "Connect"}
                            </Button>
                            {telegramTokenSet && (
                              <Button
                                size="sm"
                                variant="outline"
                                disabled={telegramSaving}
                                onClick={() => {
                                  setTelegramEditingToken(false);
                                  setTelegramTokenInput("");
                                }}
                              >
                                Cancel
                              </Button>
                            )}
                          </div>
                        </div>
                      )}
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

// ---------------------------------------------------------------------------
// VcsConnectForm — shared GitHub/GitLab connection form
// ---------------------------------------------------------------------------

interface VcsFormValues {
  base_url: string;
  token: string;
  webhook_secret: string;
}

// VcsConnectForm is the token/webhook_secret (+ base_url for GitLab) input
// shared by the GitHub and GitLab cards. Every field is optional per save —
// see handleSaveVcsConfig's comment: a blank field means "leave whatever is
// already stored", not "clear it" (an explicit empty string, which this
// form never sends, is what clears a field).
function VcsConnectForm({
  provider,
  values,
  saving,
  onChange,
  onSave,
  onCancel,
  showCancel,
}: {
  provider: "github" | "gitlab";
  values: VcsFormValues;
  saving: boolean;
  onChange: (next: VcsFormValues) => void;
  onSave: () => void;
  onCancel?: () => void;
  showCancel?: boolean;
}) {
  const canSave =
    !saving &&
    (values.token.trim() !== "" ||
      values.webhook_secret.trim() !== "" ||
      (provider === "gitlab" && values.base_url.trim() !== ""));

  return (
    <div className="space-y-2">
      {provider === "gitlab" && (
        <Input
          placeholder="https://git.example.com"
          value={values.base_url}
          onChange={(e) => onChange({ ...values, base_url: e.target.value })}
          disabled={saving}
          className="font-mono text-xs"
        />
      )}
      <Input
        type="password"
        placeholder={provider === "github" ? "Personal access token" : "Access token"}
        value={values.token}
        onChange={(e) => onChange({ ...values, token: e.target.value })}
        disabled={saving}
        className="font-mono text-xs"
      />
      <Input
        type="password"
        placeholder="Webhook secret"
        value={values.webhook_secret}
        onChange={(e) => onChange({ ...values, webhook_secret: e.target.value })}
        disabled={saving}
        className="font-mono text-xs"
      />
      <div className="flex gap-2">
        <Button size="sm" onClick={onSave} disabled={!canSave}>
          {saving ? "Saving…" : "Save"}
        </Button>
        {showCancel && onCancel && (
          <Button size="sm" variant="outline" disabled={saving} onClick={onCancel}>
            Cancel
          </Button>
        )}
      </div>
    </div>
  );
}
