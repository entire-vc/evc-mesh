import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";

export interface RelayDoc {
  title: string;
  path: string;
  relay_url: string;
}

interface TrSearchResponse {
  docs: RelayDoc[];
}

// Used while the backend endpoint isn't deployed (dev mode only)
const MOCK_DOCS: RelayDoc[] = [
  {
    title: "Project Architecture",
    path: "vault/Architecture.md",
    relay_url: "relay://mock.relay/shares/demo/docs/Architecture.md",
  },
  {
    title: "API Design Notes",
    path: "vault/API Design.md",
    relay_url: "relay://mock.relay/shares/demo/docs/API%20Design.md",
  },
  {
    title: "Sprint Planning 2026-05",
    path: "vault/Sprint/2026-05.md",
    relay_url: "relay://mock.relay/shares/demo/docs/Sprint/2026-05.md",
  },
];

export function useRelayDocSearch(query: string, shareId: string) {
  const [results, setResults] = useState<RelayDoc[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (!shareId) {
      setResults([]);
      return;
    }

    if (debounceRef.current) clearTimeout(debounceRef.current);

    debounceRef.current = setTimeout(async () => {
      if (!mountedRef.current) return;
      setIsLoading(true);
      setError(null);

      try {
        const data = await api<TrSearchResponse>("/api/v1/tr/search", {
          params: { q: query, share_id: shareId, limit: 20 },
        });
        if (mountedRef.current) setResults(data.docs ?? []);
      } catch (err) {
        if (!mountedRef.current) return;
        // While backend isn't deployed, fall back to mock data in dev
        if (import.meta.env.DEV) {
          const filtered = query
            ? MOCK_DOCS.filter(
                (d) =>
                  d.title.toLowerCase().includes(query.toLowerCase()) ||
                  d.path.toLowerCase().includes(query.toLowerCase()),
              )
            : MOCK_DOCS;
          setResults(filtered);
        } else {
          setError(err instanceof Error ? err.message : "Search failed");
          setResults([]);
        }
      } finally {
        if (mountedRef.current) setIsLoading(false);
      }
    }, 300);

    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, shareId]);

  return { results, isLoading, error };
}
