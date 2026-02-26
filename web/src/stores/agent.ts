import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  Agent,
  AgentType,
  PaginatedResponse,
  RegisterAgentRequest,
  RegisterAgentResponse,
} from "@/types";

interface RegenerateKeyResponse {
  agent: Agent;
  api_key: string;
}

interface AgentState {
  agents: Agent[];
  isLoading: boolean;

  fetchAgents: (workspaceId: string) => Promise<void>;
  registerAgent: (
    workspaceId: string,
    req: RegisterAgentRequest,
  ) => Promise<RegisterAgentResponse>;
  fetchAgent: (agentId: string) => Promise<Agent>;
  updateAgent: (
    agentId: string,
    req: { name?: string; agent_type?: AgentType; profile_description?: string; callback_url?: string },
  ) => Promise<Agent>;
  deleteAgent: (agentId: string) => Promise<void>;
  regenerateKey: (agentId: string) => Promise<RegenerateKeyResponse>;
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
      set({ agents: response.items ?? [], isLoading: false });
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

  updateAgent: async (
    agentId: string,
    req: { name?: string; agent_type?: AgentType; profile_description?: string; callback_url?: string },
  ): Promise<Agent> => {
    const agent = await api<Agent>(`/api/v1/agents/${agentId}`, {
      method: "PATCH",
      body: req,
    });
    set((state) => ({
      agents: state.agents.map((a) => (a.id === agentId ? agent : a)),
    }));
    return agent;
  },

  deleteAgent: async (agentId: string): Promise<void> => {
    await api(`/api/v1/agents/${agentId}`, { method: "DELETE" });
    set((state) => ({
      agents: state.agents.filter((a) => a.id !== agentId),
    }));
  },

  regenerateKey: async (agentId: string): Promise<RegenerateKeyResponse> => {
    const response = await api<RegenerateKeyResponse>(
      `/api/v1/agents/${agentId}/regenerate-key`,
      { method: "POST" },
    );
    set((state) => ({
      agents: state.agents.map((a) =>
        a.id === agentId ? response.agent : a,
      ),
    }));
    return response;
  },
}));
