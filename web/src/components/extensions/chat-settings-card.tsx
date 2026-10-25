import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import { AgentBadge } from "@/components/chat/chat-sidebar";
import { getChatProjectSettings, listChatAgents, putChatProjectSettings } from "@/lib/chat/api";
import { setChatEnabled } from "@/lib/chat/use-chat-enabled";
import type { ChatAgent, ChatProjectSettings } from "@/lib/chat/types";

const ROLES = ["admin", "member", "viewer"] as const;
const DEFAULT_ROLES = ["admin", "member"];

/**
 * Chat with Agent in the project's «Расширения» tab: on/off, which agents the
 * chat offers (the picker lists project agents that a runner serves) and which
 * project roles may use it. Talks to the extension's own settings endpoint, so
 * it is not the generic binding card.
 */
export function ChatSettingsCard({ projectId, chatPath }: { projectId: string; chatPath: string }) {
  const { t } = useTranslation("chat");
  const [saved, setSaved] = useState<ChatProjectSettings | null>(null);
  const [agents, setAgents] = useState<ChatAgent[]>([]);
  const [enabled, setEnabled] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [roles, setRoles] = useState<string[]>(DEFAULT_ROLES);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);

  // Only the newest load may fill the card: a slow answer for the project
  // we just left must not land in (or be saved to) the one on screen.
  const loadId = useRef(0);
  const load = useCallback(() => {
    const mine = ++loadId.current;
    setFailed(false);
    setSaved(null);
    Promise.all([getChatProjectSettings(projectId), listChatAgents(projectId)])
      .then(([s, a]) => {
        if (mine !== loadId.current) return;
        setSaved(s);
        setAgents(a);
        setEnabled(s.enabled);
        setPicked(s.agents);
        setRoles(s.allowed_roles ?? DEFAULT_ROLES);
      })
      .catch(() => mine === loadId.current && setFailed(true));
  }, [projectId]);
  useEffect(() => {
    load();
    return () => {
      loadId.current += 1;
    };
  }, [load]);

  if (failed) {
    return (
      <Card data-testid="chat-settings-error">
        <CardContent className="flex flex-col items-start gap-3 py-6">
          <p role="alert" className="flex items-center gap-2 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" aria-hidden />
            {t("settings.loadFailed")}
          </p>
          <Button variant="outline" size="sm" onClick={load}>
            {t("settings.retry")}
          </Button>
        </CardContent>
      </Card>
    );
  }
  if (!saved) return <Skeleton className="h-48 w-full" />;

  const toggle = (list: string[], value: string) =>
    list.includes(value) ? list.filter((x) => x !== value) : [...list, value];
  const needAgent = enabled && picked.length === 0;

  const save = async () => {
    const forLoad = loadId.current;
    setBusy(true);
    try {
      const out = await putChatProjectSettings(projectId, { enabled, agents: picked, allowed_roles: roles });
      if (forLoad !== loadId.current) return;
      setSaved(out);
      setChatEnabled(projectId, out.enabled && out.agents.length > 0);
      toast.success(t("settings.saved"));
    } catch (err) {
      toast.error(apiErrorMessage(err, t("settings.saveFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card data-testid="chat-settings">
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>{t("settings.title")}</CardTitle>
          {saved.enabled && (
            <Link to={chatPath} className="text-sm underline">
              {t("settings.open")}
            </Link>
          )}
        </div>
        <CardDescription>{t("settings.intro")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <label className="flex items-center gap-2 text-sm font-medium">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            data-testid="chat-settings-enabled"
          />
          {t("settings.enable")}
        </label>

        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">{t("settings.agents")}</legend>
          <p className="text-xs text-muted-foreground">{t("settings.agentsHint")}</p>
          {agents.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("settings.noAgents")}</p>
          ) : (
            <ul className="space-y-1" data-testid="chat-settings-agents">
              {agents.map((a) => (
                <li key={a.agent_id}>
                  <label className="flex items-center gap-2 rounded-md px-1 py-1 text-sm hover:bg-muted">
                    <input
                      type="checkbox"
                      checked={picked.includes(a.agent_id)}
                      onChange={() => setPicked((p) => toggle(p, a.agent_id))}
                      data-testid={`chat-settings-agent-${a.agent_id}`}
                    />
                    <AgentBadge agent={a} size="sm" />
                    <span className="truncate">{a.name}</span>
                    <span className="text-xs text-muted-foreground">{t(`sidebar.presence.${a.presence}`)}</span>
                  </label>
                </li>
              ))}
            </ul>
          )}
        </fieldset>

        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">{t("settings.roles")}</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {ROLES.map((r) => (
              <label key={r} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={roles.includes(r)}
                  onChange={() => setRoles((cur) => toggle(cur, r))}
                  data-testid={`chat-settings-role-${r}`}
                />
                {t(`settings.role.${r}`)}
              </label>
            ))}
          </div>
        </fieldset>

        {needAgent && (
          <p role="status" className="text-sm text-warning">
            {t("settings.needAgent")}
          </p>
        )}
        <Button onClick={() => void save()} disabled={busy || needAgent} data-testid="chat-settings-save">
          {t("settings.save")}
        </Button>
      </CardContent>
    </Card>
  );
}
