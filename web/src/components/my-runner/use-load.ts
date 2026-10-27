import { useCallback, useEffect, useRef, useState } from "react";

export type Loaded<T> =
  | { state: "loading" }
  | { state: "error" }
  | { state: "ready"; data: T };

/**
 * One fetch with a retry; a stale answer never overwrites a newer one. A change of `key` fetches again,
 * and a result loaded for another key reads as loading, so a workspace switch never shows the old rows.
 */
export function useLoad<T>(fetcher: () => Promise<T>, key?: string): { result: Loaded<T>; reload: () => void } {
  const [loaded, setLoaded] = useState<{ key?: string; result: Loaded<T> }>({ key, result: { state: "loading" } });
  const [nonce, setNonce] = useState(0);
  const fetchRef = useRef(fetcher);
  fetchRef.current = fetcher;

  useEffect(() => {
    let cancelled = false;
    setLoaded({ key, result: { state: "loading" } });
    fetchRef.current().then(
      (data) => { if (!cancelled) setLoaded({ key, result: { state: "ready", data } }); },
      () => { if (!cancelled) setLoaded({ key, result: { state: "error" } }); },
    );
    return () => { cancelled = true; };
  }, [nonce, key]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  const result: Loaded<T> = loaded.key === key ? loaded.result : { state: "loading" };
  return { result, reload };
}
