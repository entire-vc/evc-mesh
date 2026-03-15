import { create } from "zustand";
import { api } from "@/lib/api";
import type { Memory, ScoredMemory } from "@/types";

interface MemoryState {
  memories: Memory[];
  searchResults: ScoredMemory[];
  loading: boolean;
  searchQuery: string;

  fetchMemories: (workspaceId: string, scope?: string) => Promise<void>;
  searchMemories: (
    query: string,
    workspaceId: string,
    scope?: string,
  ) => Promise<void>;
  createMemory: (data: Partial<Memory>) => Promise<void>;
  deleteMemory: (id: string) => Promise<void>;
  setSearchQuery: (query: string) => void;
}

export const useMemoryStore = create<MemoryState>((set) => ({
  memories: [],
  searchResults: [],
  loading: false,
  searchQuery: "",

  fetchMemories: async (workspaceId: string, scope?: string) => {
    set({ loading: true });
    try {
      const params: Record<string, string> = { workspace_id: workspaceId };
      if (scope && scope !== "all") params.scope = scope;
      const response = await api<Memory[]>("/api/v1/memories", { params });
      set({ memories: response ?? [], loading: false });
    } catch {
      set({ loading: false });
    }
  },

  searchMemories: async (
    query: string,
    workspaceId: string,
    scope?: string,
  ) => {
    set({ loading: true });
    try {
      const params: Record<string, string> = {
        q: query,
        workspace_id: workspaceId,
      };
      if (scope && scope !== "all") params.scope = scope;
      const response = await api<ScoredMemory[]>("/api/v1/memories/search", {
        params,
      });
      set({ searchResults: response ?? [], loading: false });
    } catch {
      set({ loading: false });
    }
  },

  createMemory: async (data: Partial<Memory>) => {
    const memory = await api<Memory>("/api/v1/memories", {
      method: "POST",
      body: data,
    });
    set((state) => ({ memories: [memory, ...state.memories] }));
  },

  deleteMemory: async (id: string) => {
    await api(`/api/v1/memories/${id}`, { method: "DELETE" });
    set((state) => ({
      memories: state.memories.filter((m) => m.id !== id),
      searchResults: state.searchResults.filter((m) => m.id !== id),
    }));
  },

  setSearchQuery: (query: string) => {
    set({ searchQuery: query });
  },
}));
