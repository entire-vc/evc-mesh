// The workspace statistics page (/w/:ws/stats): the whole setup lives in the URL (?m=&g=&s=&p=&w=&f.*=), so a
// pasted link opens the same slice and «Add to dashboard» seeds a widget with it. The panels themselves are
// core/agent-usage; this page is only the URL, the header actions and the row links into Mesh pages.
import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { Copy, Download, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import { addToHomeDashboard, downloadStatsCsv } from "@/core/agent-usage/data";
import { Controls } from "@/core/agent-usage/controls";
import { useST } from "@/core/agent-usage/format";
import { StatsBody, sanitize, useCapabilities } from "@/core/agent-usage/stats-body";
import { stateFromSearch, stateToSearch, type StatsState } from "@/core/agent-usage/state";
import type { UsageRow } from "@/core/agent-usage/types";
import { useAgentStore } from "@/stores/agent";
import { useProjectStore } from "@/stores/project";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { OrgChartAgentNode } from "@/types";

function flattenAgentTree(nodes: OrgChartAgentNode[]): OrgChartAgentNode[] {
  const out: OrgChartAgentNode[] = [];
  const walk = (list: OrgChartAgentNode[]) => {
    for (const n of list) {
      out.push(n);
      walk(n.children);
    }
  };
  walk(nodes);
  return out;
}

export function AgentStatsPage() {
  const st = useST();
  const navigate = useNavigate();
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  const ws = useWorkspaceStore((s) => s.currentWorkspace);
  const caps = useCapabilities();
  const agents = useAgentStore((s) => s.agents);
  const fetchAgents = useAgentStore((s) => s.fetchAgents);
  const projects = useProjectStore((s) => s.projects);
  const fetchProjects = useProjectStore((s) => s.fetchProjects);
  const orgChart = useRulesStore((s) => s.orgChart);
  const fetchOrgChart = useRulesStore((s) => s.fetchOrgChart);
  const [csvBusy, setCsvBusy] = useState(false);
  const [dashBusy, setDashBusy] = useState(false);

  const raw = useMemo(() => stateFromSearch(searchParams), [searchParams]);
  const state = useMemo(() => sanitize(raw, caps), [raw, caps]);

  // Row links and filter chips read names: the agent list, the project list and the org chart (an agent's page
  // slug) are one request each, already cached by the stores other pages share.
  useEffect(() => {
    if (!ws) return;
    if (!agents.length) void fetchAgents(ws.id).catch(() => {});
    if (!projects.length) void fetchProjects(ws.id).catch(() => {});
    if (!orgChart) void fetchOrgChart(ws.id).catch(() => {});
  }, [ws, agents.length, projects.length, orgChart, fetchAgents, fetchProjects, fetchOrgChart]);

  const onChange = (next: StatsState) => setSearchParams(stateToSearch(next), { replace: true });

  const agentSlug = useMemo(() => {
    const m = new Map<string, string>();
    if (orgChart) for (const n of flattenAgentTree(orgChart.agent_tree)) if (n.slug) m.set(n.id, n.slug);
    return m;
  }, [orgChart]);
  const projectSlug = useMemo(() => new Map(projects.map((p) => [p.id, p.slug])), [projects]);

  const statsHref = (s: StatsState) => `/w/${wsSlug}/stats?${stateToSearch(s).toString()}`;

  const copyLink = async (s: StatsState) => {
    const url = `${window.location.origin}${statsHref(s)}`;
    try {
      await navigator.clipboard.writeText(url);
      toast.success(st("link.copied", "Link copied"));
    } catch {
      toast.error(st("link.copyFailed", "Could not copy the link"));
    }
  };

  const downloadCsv = async () => {
    if (!ws) return;
    setCsvBusy(true);
    try {
      await downloadStatsCsv(ws.id, state);
    } catch (err) {
      toast.error(apiErrorMessage(err, st("csv.failed", "Could not download the CSV")));
    } finally {
      setCsvBusy(false);
    }
  };

  const addWidget = async () => {
    if (!ws) return;
    setDashBusy(true);
    try {
      const doc = await addToHomeDashboard(ws.id, state);
      toast.success(st("addTo.done", "Added to «{{title}}»", { title: doc.name }));
    } catch (err) {
      if (err instanceof Error && err.message === "no dashboard") {
        toast.error(st("addTo.noHome", "No dashboard to add to: create one first"));
      } else {
        toast.error(apiErrorMessage(err, st("addTo.failed", "Could not add the widget")));
      }
    } finally {
      setDashBusy(false);
    }
  };

  return (
    <div className="mx-auto flex w-full max-w-[1600px] flex-col gap-4 pb-10 @container">
      <header className="flex min-w-0 items-center justify-between gap-3">
        <div className="min-w-0 flex-1">
          <p className="hidden truncate text-xs text-muted-foreground md:block">
            {ws?.name ?? wsSlug} / {st("title", "Agent statistics")}
          </p>
          <h1 className="truncate text-lg font-semibold md:text-2xl">{st("title", "Agent statistics")}</h1>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button size="sm" className="px-2 sm:px-3" variant="outline" onClick={() => void copyLink(state)} data-testid="copy-link">
            <Copy className="h-4 w-4 sm:mr-1" aria-hidden="true" />
            <span className="sr-only sm:not-sr-only">{st("link.copy", "Copy link")}</span>
          </Button>
          <Button size="sm" className="px-2 sm:px-3" variant="outline" onClick={() => void downloadCsv()} disabled={csvBusy} data-testid="download-csv">
            <Download className="h-4 w-4 sm:mr-1" aria-hidden="true" />
            <span className="sr-only sm:not-sr-only">{st("csv.download", "Download CSV")}</span>
          </Button>
          <Button size="sm" className="px-2 sm:px-3" onClick={() => void addWidget()} disabled={dashBusy} data-testid="add-to-dashboard">
            <Plus className="h-4 w-4 sm:mr-1" aria-hidden="true" />
            <span className="sr-only sm:not-sr-only">{st("addTo.add", "Add to dashboard")}</span>
          </Button>
        </div>
      </header>

      <Controls state={state} onChange={onChange} caps={caps} st={st} />
      <StatsBody
        state={state}
        onChange={onChange}
        mode="page"
        st={st}
        links={{
          openHref: (link: NonNullable<UsageRow["link"]>) =>
            link.kind === "agent"
              ? agentSlug.has(link.id)
                ? `/w/${wsSlug}/team/agent/${agentSlug.get(link.id)}`
                : undefined
              : link.kind === "project"
                ? projectSlug.has(link.id)
                  ? `/w/${wsSlug}/p/${projectSlug.get(link.id)}`
                  : undefined
                : undefined,
          open: (href: string) => navigate(href),
          copySlice: (s: StatsState) => void copyLink(s),
        }}
      />
    </div>
  );
}
