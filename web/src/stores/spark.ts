import { create } from "zustand";
import { api } from "@/lib/api";
import type { SparkAgentManifest, SparkInstallResponse } from "@/types";
import { apiErrorMessage } from "@/lib/api-error";

interface SparkState {
  // Catalog state
  agents: SparkAgentManifest[];
  popularAgents: SparkAgentManifest[];
  selectedAgent: SparkAgentManifest | null;
  isLoading: boolean;
  isInstalling: boolean;
  error: string | null;

  // Last install result (API key shown once)
  lastInstallResult: SparkInstallResponse | null;

  // Actions
  // workspaceId is optional on the read actions for backward compat with any
  // caller that doesn't have one yet, but the integrations page always has
  // one in scope (currentWorkspace) and must pass it — omitting it means
  // "whatever this deployment's env fallback is", not "the current
  // workspace's own choice of catalog / on-off state" (#4a3195a5).
  search: (query: string, tags: string[], limit?: number, agentType?: string, workspaceId?: string) => Promise<void>;
  fetchPopular: (limit?: number, workspaceId?: string) => Promise<void>;
  fetchAgent: (agentId: string, workspaceId?: string) => Promise<SparkAgentManifest | null>;
  selectAgent: (agent: SparkAgentManifest | null) => void;
  install: (sparkAgentId: string, workspaceId: string) => Promise<SparkInstallResponse>;
  clearInstallResult: () => void;
  clearError: () => void;
}

export const useSparkStore = create<SparkState>((set) => ({
  agents: [],
  popularAgents: [],
  selectedAgent: null,
  isLoading: false,
  isInstalling: false,
  error: null,
  lastInstallResult: null,

  search: async (query: string, tags: string[], limit = 20, agentType?: string, workspaceId?: string) => {
    set({ isLoading: true, error: null });
    try {
      const params: Record<string, string | number | undefined> = { limit };
      if (query) params.q = query;
      if (tags.length > 0) params.tags = tags.join(",");
      if (agentType && agentType !== "all") params.agent_type = agentType;
      if (workspaceId) params.workspace_id = workspaceId;

      const response = await api<{ items: SparkAgentManifest[]; count: number }>(
        "/api/v1/spark/agents",
        { params },
      );
      set({ agents: response.items ?? [], isLoading: false });
    } catch (err) {
      const message = apiErrorMessage(err, "Failed to search Spark catalog");
      set({ isLoading: false, error: message, agents: [] });
    }
  },

  fetchPopular: async (limit = 20, workspaceId?: string) => {
    set({ isLoading: true, error: null });
    try {
      const params: Record<string, string | number | undefined> = { limit };
      if (workspaceId) params.workspace_id = workspaceId;
      const response = await api<{ items: SparkAgentManifest[]; count: number }>(
        "/api/v1/spark/agents/popular",
        { params },
      );
      set({ popularAgents: response.items ?? [], isLoading: false });
    } catch (err) {
      const message = apiErrorMessage(err, "Failed to load popular agents");
      set({ isLoading: false, error: message, popularAgents: [] });
    }
  },

  fetchAgent: async (agentId: string, workspaceId?: string): Promise<SparkAgentManifest | null> => {
    set({ isLoading: true, error: null });
    try {
      const manifest = await api<SparkAgentManifest>(
        `/api/v1/spark/agents/${agentId}`,
        workspaceId ? { params: { workspace_id: workspaceId } } : undefined,
      );
      set({ selectedAgent: manifest, isLoading: false });
      return manifest;
    } catch (err) {
      const message = apiErrorMessage(err, "Failed to fetch agent");
      set({ isLoading: false, error: message });
      return null;
    }
  },

  selectAgent: (agent: SparkAgentManifest | null) => {
    set({ selectedAgent: agent });
  },

  install: async (
    sparkAgentId: string,
    workspaceId: string,
  ): Promise<SparkInstallResponse> => {
    set({ isInstalling: true, error: null });
    try {
      const result = await api<SparkInstallResponse>(
        `/api/v1/spark/agents/${sparkAgentId}/install`,
        {
          method: "POST",
          body: { workspace_id: workspaceId },
        },
      );
      set({ isInstalling: false, lastInstallResult: result });
      return result;
    } catch (err) {
      const message = apiErrorMessage(err, "Failed to install agent");
      set({ isInstalling: false, error: message });
      // Re-throw so caller can handle it
      throw err;
    }
  },

  clearInstallResult: () => {
    set({ lastInstallResult: null });
  },

  clearError: () => {
    set({ error: null });
  },
}));
