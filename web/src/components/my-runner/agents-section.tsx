import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { apiErrorMessage } from "@/lib/api-error";
import { agentState, parseLimit, setAgentLimit, setRuntimeManager, type OperatorAgent } from "@/lib/my-runner";

function AgentRow({ agent, onChanged }: { agent: OperatorAgent; onChanged: () => void }) {
  const { t } = useTranslation("runner");
  const [limit, setLimit] = useState(String(agent.max_concurrent_tasks));
  const [busy, setBusy] = useState(false);
  const parsed = parseLimit(limit);
  const dirty = parsed !== null && parsed !== agent.max_concurrent_tasks;
  const state = agentState(agent);

  const saveLimit = async () => {
    if (parsed === null) return;
    setBusy(true);
    try {
      await setAgentLimit(agent.agent_id, parsed);
      toast.success(t("agents.saved"));
      onChanged();
    } catch (e) {
      toast.error(apiErrorMessage(e, t("agents.saveFailed")));
    } finally {
      setBusy(false);
    }
  };

  const toggleManager = async (granted: boolean) => {
    setBusy(true);
    try {
      await setRuntimeManager(agent.agent_id, granted);
      toast.success(t(granted ? "agents.managerGranted" : "agents.managerRevoked", { name: agent.name }));
      onChanged();
    } catch (e) {
      toast.error(apiErrorMessage(e, t("agents.managerFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <li className="space-y-3 rounded-lg border border-border p-3" data-testid={`my-runner-agent-${agent.agent_id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="min-w-0 truncate font-medium">{agent.name}</span>
        {state === "none" ? (
          <Badge variant="outline">{t("agents.noRunner")}</Badge>
        ) : (
          <Badge variant={state === "live" ? "success" : "warning"}>
            {agent.controller?.name} · {t(state === "live" ? "agents.runnerLive" : "agents.runnerLapsed")}
          </Badge>
        )}
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="space-y-1 text-sm">
          <span className="block text-muted-foreground">{t("agents.limit")}</span>
          <Input
            inputMode="numeric"
            value={limit}
            onChange={(e) => setLimit(e.target.value)}
            aria-invalid={parsed === null}
            aria-label={`${t("agents.limit")}: ${agent.name}`}
            className="h-9 w-28"
          />
        </label>
        <Button size="sm" disabled={!dirty || busy} onClick={() => void saveLimit()}>{t("agents.save")}</Button>
        {parsed === null && <span role="alert" className="text-xs text-destructive">{t("agents.limitInvalid")}</span>}
      </div>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={agent.runtime_manager}
          disabled={busy}
          onChange={(e) => void toggleManager(e.target.checked)}
          className="mt-1 h-4 w-4 accent-primary"
          aria-label={`${t("agents.manager")}: ${agent.name}`}
        />
        <span>
          {t("agents.manager")}
          <span className="block text-xs text-muted-foreground">{t("agents.managerHint")}</span>
        </span>
      </label>
    </li>
  );
}

export function AgentsSection({ result, reload }: { result: Loaded<OperatorAgent[]>; reload: () => void }) {
  const { t } = useTranslation("runner");
  return (
    <SectionShell title={t("agents.title")} testId="my-runner-agents" result={result} onRetry={reload}>
      {(agents) =>
        agents.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="my-runner-agents-empty">{t("agents.empty")}</p>
        ) : (
          <ul className="grid gap-3 lg:grid-cols-2">
            {agents.map((a) => <AgentRow key={a.agent_id} agent={a} onChanged={reload} />)}
          </ul>
        )
      }
    </SectionShell>
  );
}
