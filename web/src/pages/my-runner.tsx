import { useTranslation } from "react-i18next";
import { RunnerAdminCard } from "@/components/my-runner/admin-card";
import { AgentsSection } from "@/components/my-runner/agents-section";
import { HistorySection } from "@/components/my-runner/history-section";
import { ResourcesSection } from "@/components/my-runner/resources-section";
import { RunnersSection } from "@/components/my-runner/runners-section";
import { useLoad } from "@/components/my-runner/use-load";
import { getOperatorAgents, getOperatorRunners, loadHistory, loadResources } from "@/lib/my-runner";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";

/** "My Runner": the signed-in user's agents, resources, runners and revision history. */
export function MyRunnerPage() {
  const { t } = useTranslation("runner");
  const me = useAuthStore((s) => s.user?.id);
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const agents = useLoad(getOperatorAgents);
  const runners = useLoad(getOperatorRunners);
  // The workspace runner catalog when the workspace has one, otherwise the user's own document.
  const runtime = useLoad(() => loadResources(wsId), wsId);
  const history = useLoad(() => loadHistory(wsId), wsId);

  const reloadAll = () => { agents.reload(); runners.reload(); history.reload(); };

  return (
    <div className="mx-auto w-full max-w-5xl space-y-5 p-4 md:p-6" data-testid="my-runner-page">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold">{t("title")}</h1>
        <p className="text-sm text-muted-foreground">{t("subtitle")}</p>
      </header>
      {/* Keyed so a workspace switch starts the card (role, extension catalog) from scratch. */}
      <RunnerAdminCard key={wsId} />
      <AgentsSection result={agents.result} reload={reloadAll} />
      <RunnersSection result={runners.result} reload={runners.reload} />
      <ResourcesSection result={runtime.result} reload={() => { runtime.reload(); history.reload(); }} />
      <HistorySection
        result={history.result}
        reload={history.reload}
        me={me}
        agents={agents.result.state === "ready" ? agents.result.data : []}
      />
    </div>
  );
}
