import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  Agent,
  PaginatedResponse,
  RegisterAgentRequest,
  RegisterAgentResponse,
} from "@/types";

interface AgentState {
  agents: Agent[];
  isLoading: boolean;

  fetchAgents: (workspaceId: string) => Promise<void>;
  registerAgent: (
    workspaceId: string,
    req: RegisterAgentRequest,
  ) => Promise<RegisterAgentResponse>;
  fetchAgent: (agentId: string) => Promise<Agent>;
}

export const useAgentStore = create<AgentState>((set) => ({
  agents: [],
  isLoading: false,

  fetchAgents: async (workspaceId: string) => {
    set({ isLoading: true });
    try {
      const response = await api<PaginatedResponse<Agent>>(
        `/api/v1/workspaces/${workspaceId}/agents`,
      );
      set({ agents: response.items, isLoading: false });
    } catch {
      set({ isLoading: false });
    }
  },

  registerAgent: async (
    workspaceId: string,
    req: RegisterAgentRequest,
  ): Promise<RegisterAgentResponse> => {
    const response = await api<RegisterAgentResponse>(
      `/api/v1/workspaces/${workspaceId}/agents`,
      { method: "POST", body: req },
    );
    set((state) => ({
      agents: [...state.agents, response.agent],
    }));
    return response;
  },

  fetchAgent: async (agentId: string): Promise<Agent> => {
    const agent = await api<Agent>(`/api/v1/agents/${agentId}`);
    set((state) => ({
      agents: state.agents.map((a) => (a.id === agentId ? agent : a)),
    }));
    return agent;
  },
}));
