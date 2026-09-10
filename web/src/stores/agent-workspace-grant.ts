import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  AgentWorkspaceGrantWithAgent,
  AgentWorkspaceGrantWithWorkspace,
  InviteAgentGrantResponse,
  WorkspaceRole,
} from "@/types";

interface AgentWorkspaceGrantState {
  // GET /workspaces/:ws_id/agent-grants — active connections into ONE
  // workspace, for the Members tab. Includes the workspace's own home
  // agents (backfilled by migration U1) alongside any invited guests; the
  // caller distinguishes them by cross-referencing agent ids already loaded
  // via useAgentStore.fetchAgents for the same workspace.
  workspaceAgentGrants: AgentWorkspaceGrantWithAgent[];
  isLoadingWorkspaceAgentGrants: boolean;

  // GET /agents/:agent_id/workspaces — active connections FROM one agent,
  // for the agent detail card. 403 (no manage_members in the agent's home
  // workspace) is not an error to surface — see agentWorkspacesForbidden.
  agentWorkspaces: AgentWorkspaceGrantWithWorkspace[];
  isLoadingAgentWorkspaces: boolean;
  agentWorkspacesForbidden: boolean;

  fetchWorkspaceAgentGrants: (workspaceId: string) => Promise<void>;
  inviteAgentToWorkspace: (
    workspaceId: string,
    agentId: string,
    role: WorkspaceRole,
  ) => Promise<InviteAgentGrantResponse>;
  revokeAgentGrant: (workspaceId: string, grantId: string) => Promise<void>;
  fetchAgentWorkspaces: (agentId: string) => Promise<void>;
}

export const useAgentWorkspaceGrantStore = create<AgentWorkspaceGrantState>(
  (set, get) => ({
    workspaceAgentGrants: [],
    isLoadingWorkspaceAgentGrants: false,
    agentWorkspaces: [],
    isLoadingAgentWorkspaces: false,
    agentWorkspacesForbidden: false,

    fetchWorkspaceAgentGrants: async (workspaceId: string) => {
      set({ isLoadingWorkspaceAgentGrants: true });
      try {
        const resp = await api<{
          agent_grants: AgentWorkspaceGrantWithAgent[];
          count: number;
        }>(`/api/v1/workspaces/${workspaceId}/agent-grants`);
        set({
          workspaceAgentGrants: resp?.agent_grants ?? [],
          isLoadingWorkspaceAgentGrants: false,
        });
      } catch {
        set({ isLoadingWorkspaceAgentGrants: false });
      }
    },

    // The response carries no embedded agent brief (name/slug) — unlike the
    // list endpoint's AgentWorkspaceGrantWithAgent, it's a bare grant plus
    // the one-time key (see InviteAgentGrantResponse's own doc comment).
    // Re-fetching the list is what gets the agent's name/slug back into
    // workspaceAgentGrants correctly, for both the 201 (new row) and the 200
    // (AC6 reactivation, same row) case — simpler and more correct than
    // trying to splice a partial item into local state either way.
    inviteAgentToWorkspace: async (
      workspaceId: string,
      agentId: string,
      role: WorkspaceRole,
    ): Promise<InviteAgentGrantResponse> => {
      const resp = await api<InviteAgentGrantResponse>(
        `/api/v1/workspaces/${workspaceId}/agent-grants`,
        { method: "POST", body: { agent_id: agentId, role } },
      );
      await get().fetchWorkspaceAgentGrants(workspaceId);
      return resp;
    },

    revokeAgentGrant: async (workspaceId: string, grantId: string) => {
      await api(`/api/v1/workspaces/${workspaceId}/agent-grants/${grantId}`, {
        method: "DELETE",
      });
      set((state) => ({
        workspaceAgentGrants: state.workspaceAgentGrants.filter(
          (g) => g.id !== grantId,
        ),
      }));
    },

    fetchAgentWorkspaces: async (agentId: string) => {
      // Clear immediately, not just on success — otherwise switching the
      // detail dialog from one agent to another can flash the PREVIOUS
      // agent's workspace list for the instant this fetch is in flight.
      set({
        isLoadingAgentWorkspaces: true,
        agentWorkspacesForbidden: false,
        agentWorkspaces: [],
      });
      try {
        const resp = await api<{
          workspaces: AgentWorkspaceGrantWithWorkspace[];
          count: number;
        }>(`/api/v1/agents/${agentId}/workspaces`);
        set({
          agentWorkspaces: resp?.workspaces ?? [],
          isLoadingAgentWorkspaces: false,
        });
      } catch (err) {
        const status = (err as { status?: unknown } | null)?.status;
        set({
          agentWorkspaces: [],
          isLoadingAgentWorkspaces: false,
          agentWorkspacesForbidden: status === 403,
        });
      }
    },
  }),
);
