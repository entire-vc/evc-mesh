import { create } from "zustand";
import { api } from "@/lib/api";
import type { Memory, MemoryFilter, ScoredMemory } from "@/types";

interface MemoryState {
  memories: Memory[];
  searchResults: ScoredMemory[];
  total: number;
  loading: boolean;
  searchQuery: string;

  fetchMemories: (workspaceId: string, filter?: MemoryFilter) => Promise<void>;
  searchMemories: (
    query: string,
    workspaceId: string,
    filter?: MemoryFilter,
  ) => Promise<void>;
  createMemory: (data: Partial<Memory>) => Promise<void>;
  deleteMemory: (id: string) => Promise<void>;
  setSearchQuery: (query: string) => void;
}

// buildParams maps a MemoryFilter onto the backend query params shared by both
// the list (GET /memories) and search (GET /memories/search) endpoints.
function buildParams(
  workspaceId: string,
  filter: MemoryFilter | undefined,
): Record<string, string> {
  const params: Record<string, string> = { workspace_id: workspaceId };
  if (!filter) return params;

  if (filter.scope && filter.scope !== "all") params.scope = filter.scope;

  if (filter.tags && filter.tags.length > 0) {
    const key = filter.tagsMode === "any" ? "tags_any" : "tags";
    params[key] = filter.tags.join(",");
  }

  if (filter.sourceType) params.source_type = filter.sourceType;
  if (filter.sourceType === "agent" && filter.createdBy) {
    params.created_by = filter.createdBy;
  }

  // Date inputs are local YYYY-MM-DD; widen to full-day RFC3339 bounds.
  if (filter.since) params.since = `${filter.since}T00:00:00Z`;
  if (filter.until) params.until = `${filter.until}T23:59:59Z`;

  if (typeof filter.relevanceMin === "number" && filter.relevanceMin > 0) {
    params.relevance_min = String(filter.relevanceMin);
  }

  if (filter.includeExpired) params.include_expired = "true";
  if (filter.includeArchived) params.include_archived = "true";

  if (filter.orderBy) {
    params.order_by = filter.orderBy;
    if (filter.orderBy === "decayed_relevance:desc") {
      params.apply_recency_decay = "true";
    }
  }

  return params;
}

export const useMemoryStore = create<MemoryState>((set) => ({
  memories: [],
  searchResults: [],
  total: 0,
  loading: false,
  searchQuery: "",

  fetchMemories: async (workspaceId: string, filter?: MemoryFilter) => {
    set({ loading: true });
    try {
      const params = buildParams(workspaceId, filter);
      const response = await api<{ items: Memory[]; total: number }>(
        "/api/v1/memories",
        { params },
      );
      set({
        memories: response.items ?? [],
        total: response.total ?? 0,
        loading: false,
      });
    } catch {
      set({ loading: false });
    }
  },

  searchMemories: async (
    query: string,
    workspaceId: string,
    filter?: MemoryFilter,
  ) => {
    set({ loading: true });
    try {
      const params = buildParams(workspaceId, filter);
      params.q = query;
      const response = await api<{ items: ScoredMemory[]; total: number }>(
        "/api/v1/memories/search",
        { params },
      );
      set({
        searchResults: response.items ?? [],
        total: response.total ?? 0,
        loading: false,
      });
    } catch {
      set({ loading: false });
    }
  },

  createMemory: async (data: Partial<Memory>) => {
    const response = await api<{ memory: Memory; outcome: string }>(
      "/api/v1/memories",
      {
        method: "POST",
        body: data,
      },
    );
    const memory = response.memory;
    set((state) => ({
      memories: state.memories.some((m) => m.id === memory.id)
        ? state.memories.map((m) => (m.id === memory.id ? memory : m))
        : [memory, ...state.memories],
    }));
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
