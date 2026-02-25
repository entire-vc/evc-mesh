import { create } from "zustand";
import { api } from "@/lib/api";
import type { SparkAgentManifest, SparkInstallResponse } from "@/types";

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
  search: (query: string, tags: string[], limit?: number) => Promise<void>;
  fetchPopular: (limit?: number) => Promise<void>;
  fetchAgent: (agentId: string) => Promise<SparkAgentManifest | null>;
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

  search: async (query: string, tags: string[], limit = 20) => {
    set({ isLoading: true, error: null });
    try {
      const params: Record<string, string | number | undefined> = { limit };
      if (query) params.q = query;
      if (tags.length > 0) params.tags = tags.join(",");

      const response = await api<{ items: SparkAgentManifest[]; count: number }>(
        "/api/v1/spark/agents",
        { params },
      );
      set({ agents: response.items ?? [], isLoading: false });
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to search Spark catalog";
      set({ isLoading: false, error: message, agents: [] });
    }
  },

  fetchPopular: async (limit = 20) => {
    set({ isLoading: true, error: null });
    try {
      const response = await api<{ items: SparkAgentManifest[]; count: number }>(
        "/api/v1/spark/agents/popular",
        { params: { limit } },
      );
      set({ popularAgents: response.items ?? [], isLoading: false });
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to load popular agents";
      set({ isLoading: false, error: message, popularAgents: [] });
    }
  },

  fetchAgent: async (agentId: string): Promise<SparkAgentManifest | null> => {
    set({ isLoading: true, error: null });
    try {
      const manifest = await api<SparkAgentManifest>(
        `/api/v1/spark/agents/${agentId}`,
      );
      set({ selectedAgent: manifest, isLoading: false });
      return manifest;
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to fetch agent";
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
      const message = err instanceof Error ? err.message : "Failed to install agent";
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
