import { useCallback, useEffect, useState } from "react";
import { useParams } from "react-router";
import { Trans, useTranslation } from "react-i18next";
import {
  CheckCircle2,
  Circle,
  GitBranch,
  GitMerge,
  Send,
  Server,
  Sparkles,
} from "lucide-react";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { RuntimeResourcesCard } from "@/components/runtime-resources-card";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { apiErrorMessage } from "@/lib/api-error";
import type {
  EffectiveIntegrationStatus,
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
  descriptionKey: string;
  nameKey?: string;
  icon: React.ComponentType<{ className?: string }>;
}

const PROVIDERS: ProviderMeta[] = [
  {
    id: "github",
    name: "GitHub",
    descriptionKey: "integrationsPage.providers.github",
    icon: GitBranch,
  },
  {
    id: "gitlab",
    name: "GitLab",
    descriptionKey: "integrationsPage.providers.gitlab",
    icon: GitMerge,
  },
  {
    id: "spark",
    name: "Spark Agent Catalog",
    nameKey: "integrationsPage.providers.sparkName",
    descriptionKey: "integrationsPage.providers.spark",
    icon: Sparkles,
  },
  {
    id: "mcp",
    name: "MCP Server",
    nameKey: "integrationsPage.providers.mcpName",
    descriptionKey: "integrationsPage.providers.mcp",
    icon: Server,
  },
  {
    id: "telegram",
    name: "Telegram",
    descriptionKey: "integrationsPage.providers.telegram",
    icon: Send,
  },
];

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function IntegrationsPage() {
  const { t } = useTranslation("integrations");
  useParams();
  const { currentWorkspace } = useWorkspaceStore();
  const { myRole, fetchMyRole } = useMemberStore();

  const [configs, setConfigs] = useState<IntegrationConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [effective, setEffective] = useState<EffectiveIntegrationStatus[]>([]);
  const [toggling, setToggling] = useState<string | null>(null);
  // Provider whose env-backed integration the admin is about to switch off.
  const [confirmDisable, setConfirmDisable] = useState<string | null>(null);

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
      const res = await api<{
        integrations: IntegrationConfig[];
        effective?: EffectiveIntegrationStatus[];
      }>(`/api/v1/workspaces/${currentWorkspace.id}/integrations`);
      setConfigs(res.integrations ?? []);
      setEffective(res.effective ?? []);
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

  const getEffective = (provider: IntegrationProvider) =>
    effective.find((e) => e.provider === provider);

  const getConfig = (provider: IntegrationProvider) =>
    configs.find((c) => c.provider === provider);

  const handleSaveTelegramToken = async () => {
    if (!currentWorkspace) return;
    const token = telegramTokenInput.trim();
    if (!token) {
      toast.error(t("integrationsPage.enterBotToken"));
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
      // effective status drives the toggle; refresh it from the server
      await fetchConfigs();
      setTelegramTokenInput("");
      setTelegramEditingToken(false);
      const botUsername = (updated.config as TelegramIntegrationConfig).bot_username;
      toast.success(
        botUsername
          ? t("integrationsPage.connectedAsToast", { username: botUsername })
          : t("integrationsPage.telegramConnectedToast"),
      );
    } catch (err) {
      toast.error(apiErrorMessage(err, t("integrationsPage.telegramSaveFailed")));
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
      toast.error(t("integrationsPage.enterOneField"));
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
      // effective status drives the toggle; refresh it from the server
      await fetchConfigs();
      setVcsFormValues((prev) => ({ ...prev, [provider]: { base_url: "", token: "", webhook_secret: "" } }));
      setVcsEditing((prev) => ({ ...prev, [provider]: false }));
      toast.success(t("integrationsPage.vcsSaved", { label }));
    } catch (err) {
      toast.error(apiErrorMessage(err, t("integrationsPage.vcsSaveFailed", { label })));
    } finally {
      setVcsSaving(null);
    }
  };

  const handleToggle = async (
    provider: IntegrationProvider,
    enabled: boolean,
    confirmEnvOverride = false,
  ) => {
    if (!currentWorkspace) return;
    const eff = getEffective(provider);
    const existing = getConfig(provider);
    // Enabling with nothing to store would create an empty placeholder row that
    // the runtime ignores while this screen claims "Enabled" — send the admin
    // to the connection form instead.
    if (enabled && !existing && (provider === "github" || provider === "gitlab")) {
      setVcsEditing((prev) => ({ ...prev, [provider]: true }));
      return;
    }
    if (enabled && !existing && !eff?.env_available) {
      toast.error(t("integrationsPage.nothingToEnable"));
      return;
    }
    setToggling(provider);
    try {
      if (existing) {
        await api<IntegrationConfig>(`/api/v1/integrations/${existing.id}`, {
          method: "PATCH",
          body: { is_active: enabled, confirm_env_override: confirmEnvOverride },
        });
      } else {
        await api<IntegrationConfig>(
          `/api/v1/workspaces/${currentWorkspace.id}/integrations`,
          {
            method: "POST",
            body: { provider, config: {}, is_active: enabled, confirm_env_override: confirmEnvOverride },
          },
        );
      }
      setConfirmDisable(null);
      await fetchConfigs();
    } catch (err) {
      toast.error(apiErrorMessage(err, t("integrationsPage.updateFailed")));
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
      await fetchConfigs();
    } catch (err) {
      toast.error(apiErrorMessage(err, t("integrationsPage.removeFailed")));
    } finally {
      setToggling(null);
    }
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 overflow-y-auto">
        <p className="mb-6 text-sm text-muted-foreground max-w-prose">
          {t("integrationsPage.intro")}
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
              const eff = getEffective(provider.id);
              // The runtime's answer when the API provides it; the raw row is
              // only a fallback for an older API.
              const isActive = eff ? eff.effective_enabled : (cfg?.is_active ?? false);
              const viaEnv = eff?.source === "env";
              const needsEnvConfirm = isActive && !!eff?.env_available;
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
                          <CardTitle className="flex flex-wrap items-center gap-2 text-base">
                            {provider.nameKey ? t(provider.nameKey) : provider.name}
                            {isActive && eff && eff.source !== "none" && (
                              <span
                                data-testid={`source-${provider.id}`}
                                className="rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] font-normal text-muted-foreground"
                              >
                                {viaEnv
                                  ? t("integrationsPage.sourceInstance")
                                  : t("integrationsPage.sourceWorkspace")}
                              </span>
                            )}
                          </CardTitle>
                          <p className="text-xs text-muted-foreground">
                            {t(provider.descriptionKey)}
                          </p>
                        </div>
                      </div>

                      <div className="flex items-center gap-2 shrink-0 ml-4">
                        {cfg && provider.id !== "mcp" && (
                          <button
                            onClick={() => void handleDelete(provider.id)}
                            className="text-xs text-muted-foreground hover:text-destructive transition-colors"
                            disabled={isLoading}
                          >
                            {t("integrationsPage.remove")}
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
                            disabled={isLoading}
                            onClick={() => {
                              if (needsEnvConfirm) {
                                setConfirmDisable(provider.id);
                                return;
                              }
                              void handleToggle(provider.id, !isActive);
                            }}
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
                                {t("integrationsPage.enabled")}
                              </span>
                            ) : (
                              <span className="flex items-center gap-1.5">
                                <Circle className="h-3.5 w-3.5" />
                                {t("integrationsPage.enable")}
                              </span>
                            )}
                          </Button>
                        ) : null}
                      </div>
                    </div>
                  </CardHeader>

                  {viaEnv && (
                    <CardContent className="pt-0">
                      <p className="text-xs text-muted-foreground" data-testid={`env-note-${provider.id}`}>
                        {t("integrationsPage.envNote")}
                      </p>
                    </CardContent>
                  )}

                  {confirmDisable === provider.id && (
                    <CardContent className="pt-0">
                      <div
                        role="alertdialog"
                        className="rounded-lg border border-destructive/40 bg-destructive/5 p-3 space-y-2"
                      >
                        <p className="text-xs">
                          {t("integrationsPage.confirmDisable.message")}
                        </p>
                        <div className="flex flex-wrap gap-2">
                          <Button
                            size="sm"
                            variant="destructive"
                            disabled={isLoading}
                            onClick={() => void handleToggle(provider.id, false, true)}
                          >
                            {t("integrationsPage.confirmDisable.turnOff")}
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => setConfirmDisable(null)}
                          >
                            {t("integrationsPage.cancel")}
                          </Button>
                        </div>
                      </div>
                    </CardContent>
                  )}

                  {provider.id === "github" && (
                    <CardContent className="pt-0 space-y-3">
                      <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                        <p className="text-xs font-medium text-muted-foreground mb-1">
                          {t("integrationsPage.connection")}
                        </p>
                        {githubCfg?.token_set || githubCfg?.webhook_secret_set ? (
                          !vcsEditing.github ? (
                            <div className="flex items-center justify-between">
                              <p className="text-xs text-muted-foreground">
                                {githubCfg.webhook_secret_set
                                  ? t("integrationsPage.github.usingTokenAndSecret")
                                  : t("integrationsPage.github.usingToken")}
                              </p>
                              <button
                                onClick={() =>
                                  setVcsEditing((prev) => ({ ...prev, github: true }))
                                }
                                className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
                              >
                                {t("integrationsPage.change")}
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
                              {t("integrationsPage.github.connectPrompt")}
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
                            {t("integrationsPage.webhookUrl")}
                          </p>
                          <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
                            {window.location.origin}/webhooks/github
                          </code>
                          <p className="mt-2 text-xs text-muted-foreground">
                            <Trans
                              t={t}
                              i18nKey="integrationsPage.github.webhookHelp"
                              components={{
                                strong: <strong />,
                                em: <em />,
                                code: (
                                  <code className="rounded bg-background px-1">
                                    MESH-{"<task-id>"}
                                  </code>
                                ),
                              }}
                            />
                          </p>
                        </div>
                      )}
                    </CardContent>
                  )}

                  {provider.id === "gitlab" && (
                    <CardContent className="pt-0 space-y-3">
                      <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                        <p className="text-xs font-medium text-muted-foreground mb-1">
                          {t("integrationsPage.connection")}
                        </p>
                        {gitlabCfg?.token_set || gitlabCfg?.webhook_secret_set || gitlabCfg?.base_url ? (
                          !vcsEditing.gitlab ? (
                            <div className="flex items-center justify-between">
                              <p className="text-xs text-muted-foreground">
                                {gitlabCfg.base_url ? (
                                  <Trans
                                    t={t}
                                    i18nKey="integrationsPage.gitlab.connectedTo"
                                    values={{ url: gitlabCfg.base_url }}
                                    components={{
                                      url: (
                                        <span className="font-medium text-foreground" />
                                      ),
                                    }}
                                  />
                                ) : (
                                  t("integrationsPage.gitlab.connected")
                                )}
                              </p>
                              <button
                                onClick={() =>
                                  setVcsEditing((prev) => ({ ...prev, gitlab: true }))
                                }
                                className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
                              >
                                {t("integrationsPage.change")}
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
                              {t("integrationsPage.gitlab.connectPrompt")}
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
                            {t("integrationsPage.webhookUrl")}
                          </p>
                          <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
                            {window.location.origin}/webhooks/gitlab
                          </code>
                          <p className="mt-2 text-xs text-muted-foreground">
                            <Trans
                              t={t}
                              i18nKey="integrationsPage.gitlab.webhookHelp"
                              components={{
                                strong: <strong />,
                                em: <em />,
                                code: (
                                  <code className="rounded bg-background px-1">
                                    MESH-{"<task-id>"}
                                  </code>
                                ),
                              }}
                            />
                          </p>
                        </div>
                      )}
                    </CardContent>
                  )}

                  {provider.id === "spark" && isActive && (
                    <CardContent className="pt-0">
                      <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
                        <p className="text-xs text-muted-foreground">
                          <Trans
                            t={t}
                            i18nKey="integrationsPage.spark.enabledNote"
                            components={{
                              link: (
                                <a
                                  href={`/w/${currentWorkspace?.slug}/spark`}
                                  className="text-primary hover:underline font-medium"
                                />
                              ),
                            }}
                          />
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
                            {t("integrationsPage.mcp.sseEndpoint")}
                          </p>
                          <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
                            {window.location.origin}/mcp/sse
                          </code>
                          <p className="text-xs text-muted-foreground mt-1.5">
                            <Trans
                              t={t}
                              i18nKey="integrationsPage.mcp.sseNote"
                              components={{
                                code: <code className="rounded bg-muted px-1" />,
                              }}
                            />
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
                          <Trans
                            t={t}
                            i18nKey="integrationsPage.mcp.registerHelp"
                            components={{
                              link: (
                                <a
                                  href={`/w/${currentWorkspace?.slug}/agents`}
                                  className="text-primary hover:underline font-medium"
                                />
                              ),
                              code: <code className="rounded bg-muted px-1" />,
                            }}
                          />
                        </p>
                      </div>
                    </CardContent>
                  )}

                  {provider.id === "telegram" && (
                    <CardContent className="pt-0">
                      {telegramTokenSet && !telegramEditingToken ? (
                        <div className="flex items-center justify-between rounded-lg border border-dashed border-border bg-muted/30 p-3">
                          <p className="text-xs text-muted-foreground">
                            <Trans
                              t={t}
                              i18nKey="integrationsPage.telegram.connectedAs"
                              values={{ username: telegramCfg?.bot_username ?? "" }}
                              components={{
                                name: <span className="font-medium text-foreground" />,
                              }}
                            />
                          </p>
                          <button
                            onClick={() => setTelegramEditingToken(true)}
                            className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
                          >
                            {t("integrationsPage.telegram.changeToken")}
                          </button>
                        </div>
                      ) : (
                        <div className="space-y-2">
                          <p className="text-xs text-muted-foreground">
                            <Trans
                              t={t}
                              i18nKey="integrationsPage.telegram.createBot"
                              components={{ bot: <span className="font-medium" /> }}
                            />
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
                              {telegramSaving
                                ? t("integrationsPage.telegram.connecting")
                                : t("integrationsPage.telegram.connect")}
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
                                {t("integrationsPage.cancel")}
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
        {currentWorkspace && <RuntimeResourcesCard workspaceId={currentWorkspace.id} />}
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
  const { t } = useTranslation("integrations");
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
        placeholder={
          provider === "github"
            ? t("integrationsPage.vcsForm.personalToken")
            : t("integrationsPage.vcsForm.accessToken")
        }
        value={values.token}
        onChange={(e) => onChange({ ...values, token: e.target.value })}
        disabled={saving}
        className="font-mono text-xs"
      />
      <Input
        type="password"
        placeholder={t("integrationsPage.vcsForm.webhookSecret")}
        value={values.webhook_secret}
        onChange={(e) => onChange({ ...values, webhook_secret: e.target.value })}
        disabled={saving}
        className="font-mono text-xs"
      />
      <div className="flex gap-2">
        <Button size="sm" onClick={onSave} disabled={!canSave}>
          {saving ? t("integrationsPage.vcsForm.saving") : t("integrationsPage.vcsForm.save")}
        </Button>
        {showCancel && onCancel && (
          <Button size="sm" variant="outline" disabled={saving} onClick={onCancel}>
            {t("integrationsPage.cancel")}
          </Button>
        )}
      </div>
    </div>
  );
}
