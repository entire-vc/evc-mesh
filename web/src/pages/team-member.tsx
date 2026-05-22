import { useEffect } from "react";
import { useNavigate, useParams } from "react-router";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { OrgChartAgentNode } from "@/types";

function flattenAgentTree(nodes: OrgChartAgentNode[]): OrgChartAgentNode[] {
  const result: OrgChartAgentNode[] = [];
  for (const n of nodes) {
    result.push(n);
    if (n.children?.length > 0) {
      result.push(...flattenAgentTree(n.children));
    }
  }
  return result;
}

export function TeamMemberPage() {
  const { wsSlug, kind, memberSlug } = useParams<{
    wsSlug: string;
    kind: string;
    memberSlug: string;
  }>();
  const navigate = useNavigate();
  const { currentWorkspace } = useWorkspaceStore();
  const { orgChart, fetchOrgChart } = useRulesStore();
  const { agents, fetchAgents } = useAgentStore();

  const orgChartUrl = `/w/${wsSlug}/org-chart`;

  useEffect(() => {
    if (currentWorkspace) {
      void fetchOrgChart(currentWorkspace.id);
      void fetchAgents(currentWorkspace.id);
    }
  }, [currentWorkspace, fetchOrgChart, fetchAgents]);

  const allAgents = orgChart ? flattenAgentTree(orgChart.agent_tree) : [];
  const matchedNode = allAgents.find((a) => a.slug === memberSlug);
  const agent = matchedNode
    ? (agents.find((a) => a.id === matchedNode.id) ?? null)
    : null;

  // Redirect: unknown kind or slug not found after data loaded
  const shouldRedirect = kind !== "agent" || (orgChart !== null && !matchedNode);
  useEffect(() => {
    if (shouldRedirect) {
      void navigate(orgChartUrl, { replace: true });
    }
  }, [shouldRedirect, orgChartUrl, navigate]);

  if (shouldRedirect) return null;

  return (
    <AgentDetailDialog
      open={true}
      onOpenChange={(open) => {
        if (!open) void navigate(orgChartUrl, { replace: true });
      }}
      agent={agent}
    />
  );
}
