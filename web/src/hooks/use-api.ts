import { useCallback, useState } from "react";
import { ApiRequestError } from "@/lib/api";

interface UseApiState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
}

interface UseApiReturn<T> extends UseApiState<T> {
  execute: (...args: unknown[]) => Promise<T | null>;
  reset: () => void;
}

export function useApi<T>(
  apiCall: (...args: never[]) => Promise<T>,
): UseApiReturn<T> {
  const [state, setState] = useState<UseApiState<T>>({
    data: null,
    loading: false,
    error: null,
  });

  const execute = useCallback(
    async (...args: unknown[]): Promise<T | null> => {
      setState((prev) => ({ ...prev, loading: true, error: null }));
      try {
        const data = await (apiCall as (...a: unknown[]) => Promise<T>)(
          ...args,
        );
        setState({ data, loading: false, error: null });
        return data;
      } catch (err) {
        const message =
          err instanceof ApiRequestError
            ? err.message
            : "An unexpected error occurred";
        setState((prev) => ({ ...prev, loading: false, error: message }));
        return null;
      }
    },
    [apiCall],
  );

  const reset = useCallback(() => {
    setState({ data: null, loading: false, error: null });
  }, []);

  return { ...state, execute, reset };
}
