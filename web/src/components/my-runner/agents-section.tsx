import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { apiErrorMessage } from "@/lib/api-error";
import { agentState, parseLimit, setAgentLimit, setRuntimeManager, type OperatorAgent } from "@/lib/my-runner";

/** Same threshold as the resource lists: below it the table is short enough to scan. */
const FILTER_FROM = 20;
/** Fleet agents carry six and more profiles; the rest is in the tooltip so the column stays narrow. */
const PROFILES_SHOWN = 2;

/** A table cell that, below md, becomes a labelled line of a stacked card. */
function Cell({ label, children, className = "" }: { label: string; children: ReactNode; className?: string }) {
  return (
    <td className={`block py-1 align-middle md:table-cell md:px-3 md:py-2 ${className}`}>
      <span className="mr-2 text-xs text-muted-foreground md:hidden">{label}:</span>
      {children}
    </td>
  );
}

function LimitEditor({ agent, onChanged }: { agent: OperatorAgent; onChanged: () => void }) {
  const { t } = useTranslation("runner");
  const [limit, setLimit] = useState(String(agent.max_concurrent_tasks));
  const [busy, setBusy] = useState(false);
  const parsed = parseLimit(limit);
  const dirty = parsed !== null && parsed !== agent.max_concurrent_tasks;

  const save = async () => {
    if (parsed === null || !dirty) return;
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

  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <Input
        inputMode="numeric"
        value={limit}
        onChange={(e) => setLimit(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") void save();
          if (e.key === "Escape") setLimit(String(agent.max_concurrent_tasks));
        }}
        aria-invalid={parsed === null}
        aria-label={`${t("agents.limit")}: ${agent.name}`}
        className="h-8 w-20"
      />
      {/* Save shows up only for a change, so an untouched table has no button per row. */}
      {dirty && <Button size="sm" disabled={busy} onClick={() => void save()}>{t("agents.save")}</Button>}
      {parsed === null && <span role="alert" className="text-xs text-destructive">{t("agents.limitInvalid")}</span>}
    </span>
  );
}

function ManagerToggle({ agent, onChanged }: { agent: OperatorAgent; onChanged: () => void }) {
  const { t } = useTranslation("runner");
  const [busy, setBusy] = useState(false);
  const toggle = async (granted: boolean) => {
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
    <input
      type="checkbox"
      checked={agent.runtime_manager}
      disabled={busy}
      onChange={(e) => void toggle(e.target.checked)}
      className="h-4 w-4 align-middle accent-primary"
      aria-label={`${t("agents.manager")}: ${agent.name}`}
      title={t("agents.managerHint")}
    />
  );
}

function AgentRow({ agent, me, profiles, onChanged }: { agent: OperatorAgent; me?: string; profiles: string[]; onChanged: () => void }) {
  const { t } = useTranslation("runner");
  const state = agentState(agent);
  // Limits and the manager assignment belong to the agent's operator; an admin
  // sees the other agents the workspace's runner drives, read-only.
  const own = !!me && agent.operator_user_id === me;
  const operator = own ? t("agents.operatorYou") : t(agent.operator_user_id ? "agents.operatorOther" : "agents.operatorNone");

  return (
    <tr className="block rounded-lg border border-border p-3 md:table-row md:rounded-none md:border-0 md:border-t md:p-0" data-testid={`my-runner-agent-${agent.agent_id}`}>
      <td className="block pb-1 font-medium break-words md:table-cell md:min-w-32 md:px-3 md:py-2">{agent.name}</td>
      <Cell label={t("agents.colRunner")}>
        {state === "none" ? (
          <Badge variant="outline">{t("agents.noRunner")}</Badge>
        ) : (
          <Badge variant={state === "live" ? "success" : "warning"} className="whitespace-nowrap">
            {agent.controller?.name} · {t(state === "live" ? "agents.runnerLive" : "agents.runnerLapsed")}
          </Badge>
        )}
      </Cell>
      <Cell label={t("agents.colOperator")}>
        <span className={own ? "" : "text-muted-foreground"} title={own ? undefined : t(agent.operator_user_id ? "agents.otherOperator" : "agents.noOperator")}>
          {operator}
        </span>
      </Cell>
      <Cell label={t("agents.colProfiles")}>
        {profiles.length === 0 ? <span className="text-muted-foreground">—</span> : (
          <span className="break-words" title={profiles.join("\n")}>
            {profiles.slice(0, PROFILES_SHOWN).join(", ")}
            {profiles.length > PROFILES_SHOWN && <span className="text-muted-foreground"> {t("agents.moreProfiles", { n: profiles.length - PROFILES_SHOWN })}</span>}
          </span>
        )}
      </Cell>
      <Cell label={t("agents.limit")}>
        {own ? <LimitEditor key={agent.max_concurrent_tasks} agent={agent} onChanged={onChanged} /> : (
          <span data-testid={`my-runner-agent-readonly-${agent.agent_id}`}>{agent.max_concurrent_tasks}</span>
        )}
      </Cell>
      <Cell label={t("agents.colManager")}>
        {own ? <ManagerToggle agent={agent} onChanged={onChanged} /> : <span className="text-muted-foreground">{agent.runtime_manager ? t("agents.yes") : "—"}</span>}
      </Cell>
    </tr>
  );
}

function AgentsTable({ agents, me, profiles, reload }: {
  agents: OperatorAgent[]; me?: string; profiles: Record<string, string[]>; reload: () => void;
}) {
  const { t } = useTranslation("runner");
  const [query, setQuery] = useState("");
  const q = agents.length > FILTER_FROM ? query.trim().toLowerCase() : "";
  const shown = [...agents]
    .sort((a, b) => a.name.localeCompare(b.name))
    .filter((a) => !q || a.name.toLowerCase().includes(q));

  return (
    <div className="space-y-3">
      {agents.length > FILTER_FROM && (
        <Input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("agents.filter")}
          aria-label={t("agents.filter")}
          className="h-9 sm:max-w-xs"
        />
      )}
      <table className="block w-full text-sm md:table">
        <thead className="hidden md:table-header-group">
          <tr className="text-left text-xs whitespace-nowrap text-muted-foreground">
            <th className="px-3 py-2 font-medium">{t("agents.colAgent")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colRunner")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colOperator")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colProfiles")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.limit")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colManager")}</th>
          </tr>
        </thead>
        <tbody className="block space-y-3 md:table-row-group md:space-y-0">
          {shown.map((a) => <AgentRow key={a.agent_id} agent={a} me={me} profiles={profiles[a.agent_id] ?? []} onChanged={reload} />)}
        </tbody>
      </table>
      {shown.length === 0 && <p className="text-sm text-muted-foreground">{t("resources.noMatches")}</p>}
      {agents.some((a) => a.operator_user_id === me) && <p className="text-xs text-muted-foreground">{t("agents.managerHint")}</p>}
    </div>
  );
}

export function AgentsSection({ result, reload, me, profiles = {} }: {
  result: Loaded<OperatorAgent[]>; reload: () => void; me?: string;
  /** Profiles each agent may run with, by agent id, from the workspace catalog's bindings. */
  profiles?: Record<string, string[]>;
}) {
  const { t } = useTranslation("runner");
  return (
    <SectionShell title={t("agents.title")} testId="my-runner-agents" result={result} onRetry={reload}>
      {(agents) =>
        agents.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="my-runner-agents-empty">{t("agents.empty")}</p>
        ) : (
          <AgentsTable agents={agents} me={me} profiles={profiles} reload={reload} />
        )
      }
    </SectionShell>
  );
}
