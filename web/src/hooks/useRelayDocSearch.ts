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

export function useRelayDocSearch(query: string, projId: string) {
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
    if (!projId) {
      setResults([]);
      return;
    }

    if (debounceRef.current) clearTimeout(debounceRef.current);

    debounceRef.current = setTimeout(async () => {
      if (!mountedRef.current) return;
      setIsLoading(true);
      setError(null);

      try {
        const data = await api<TrSearchResponse>(
          `/api/v1/projects/${projId}/tr/search`,
          { params: { q: query, limit: 20 } },
        );
        if (mountedRef.current) setResults(data.docs ?? []);
      } catch (err) {
        if (!mountedRef.current) return;
        setError(err instanceof Error ? err.message : "Search failed");
        setResults([]);
      } finally {
        if (mountedRef.current) setIsLoading(false);
      }
    }, 300);

    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, projId]);

  return { results, isLoading, error };
}
