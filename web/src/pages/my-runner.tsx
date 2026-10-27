import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { RunnerAdminCard } from "@/components/my-runner/admin-card";
import { RunnerProgramCard } from "@/components/my-runner/program-card";
import { AgentsSection } from "@/components/my-runner/agents-section";
import { HistorySection } from "@/components/my-runner/history-section";
import { ResourcesSection } from "@/components/my-runner/resources-section";
import { RunnersSection } from "@/components/my-runner/runners-section";
import { RunnerTabList, useRunnerTab } from "@/components/my-runner/runner-tabs";
import { useLoad } from "@/components/my-runner/use-load";
import {
  getOperatorAgents, getOperatorRunners, loadHistory, loadResources, profilesByAgent, type OperatorAgent, type OperatorRunner,
} from "@/lib/my-runner";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";

/**
 * The Runner section: overview (the on/off switch, runners, controllers),
 * agents, resources and revision history, one tab each.
 */
export function MyRunnerPage() {
  const { t } = useTranslation("runner");
  const [tab, selectTab] = useRunnerTab();
  const me = useAuthStore((s) => s.user?.id);
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  // Until the workspace is known the lists stay loading; its id arriving fetches them.
  const pending = <T,>() => new Promise<T>(() => {});
  const agents = useLoad(() => (wsId ? getOperatorAgents(wsId) : pending<OperatorAgent[]>()), wsId);
  const runners = useLoad(() => (wsId ? getOperatorRunners(wsId) : pending<OperatorRunner[]>()), wsId);
  // The workspace runner catalog when the workspace has one, otherwise the user's own document.
  const runtime = useLoad(() => loadResources(wsId), wsId);
  const history = useLoad(() => loadHistory(wsId), wsId);

  // Whose resources an edit changes is known only once they load: the workspace catalog
  // (seen and applied by everyone) or the user's own document. Until then no claim is made.
  const subtitleKey = runtime.result.state !== "ready" ? "subtitle.neutral"
    : runtime.result.data.source === "workspace" ? "subtitle.workspace" : "subtitle.personal";

  const reloadAll = () => { agents.reload(); runners.reload(); history.reload(); };
  const catalog = runtime.result.state === "ready" && runtime.result.data.source === "workspace" ? runtime.result.data.config : null;
  const profiles = useMemo(() => profilesByAgent(catalog), [catalog]);

  return (
    <div className="mx-auto w-full max-w-5xl space-y-5 p-4 md:p-6" data-testid="my-runner-page">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">{t("title")}</h1>
        <p className="text-sm text-muted-foreground" data-testid="my-runner-subtitle">{t(subtitleKey)}</p>
      </header>
      <RunnerTabList tab={tab} onSelect={selectTab} />
      <div role="tabpanel" id={`my-runner-panel-${tab}`} aria-labelledby={`my-runner-tab-${tab}`} className="space-y-5">
        {tab === "overview" && (
          <>
            {/* Keyed so a workspace switch starts the card (role, extension catalog) from scratch. */}
            <RunnerAdminCard key={wsId} />
            <RunnerProgramCard key={`program-${wsId}`} />
            <RunnersSection result={runners.result} reload={runners.reload} wsId={wsId} />
          </>
        )}
        {tab === "agents" && <AgentsSection result={agents.result} reload={reloadAll} me={me} profiles={profiles} />}
        {tab === "resources" && <ResourcesSection result={runtime.result} reload={() => { runtime.reload(); history.reload(); }} />}
        {tab === "history" && (
          <HistorySection
            result={history.result}
            reload={history.reload}
            me={me}
            agents={agents.result.state === "ready" ? agents.result.data : []}
            wsId={wsId}
            onRestored={() => { runtime.reload(); history.reload(); }}
          />
        )}
      </div>
    </div>
  );
}
