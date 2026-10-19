import { useCallback, useEffect, useRef, useState } from "react";

export type Loaded<T> =
  | { state: "loading" }
  | { state: "error" }
  | { state: "ready"; data: T };

/** One fetch with a retry; a stale answer never overwrites a newer one. */
export function useLoad<T>(fetcher: () => Promise<T>): { result: Loaded<T>; reload: () => void } {
  const [result, setResult] = useState<Loaded<T>>({ state: "loading" });
  const [nonce, setNonce] = useState(0);
  const fetchRef = useRef(fetcher);
  fetchRef.current = fetcher;

  useEffect(() => {
    let cancelled = false;
    setResult({ state: "loading" });
    fetchRef.current().then(
      (data) => { if (!cancelled) setResult({ state: "ready", data }); },
      () => { if (!cancelled) setResult({ state: "error" }); },
    );
    return () => { cancelled = true; };
  }, [nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { result, reload };
}
