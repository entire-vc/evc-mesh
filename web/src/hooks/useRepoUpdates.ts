import { useCallback, useEffect, useState } from "react";
import { ApiRequestError } from "@/lib/api";
import { listRepoUpdates, type RepoUpdate } from "@/lib/extension-repo";

export interface RepoUpdatesState {
  status: "loading" | "ready" | "forbidden" | "error";
  updates: RepoUpdate[];
  /** Updates that can be taken now or are waiting for consent. */
  count: number;
  reload: () => void;
}

/** Updates of repo-installed extensions. `null` = not yet known who the instance administrator is. */
export function useRepoUpdates(isInstanceAdmin: boolean | null): RepoUpdatesState {
  const [status, setStatus] = useState<RepoUpdatesState["status"]>("loading");
  const [updates, setUpdates] = useState<RepoUpdate[]>([]);
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    if (isInstanceAdmin === null) return;
    if (!isInstanceAdmin) {
      setStatus("forbidden");
      return;
    }
    let live = true;
    setStatus("loading");
    listRepoUpdates().then(
      (r) => {
        if (!live) return;
        setUpdates(r.updates);
        setStatus("ready");
      },
      (err) => {
        if (!live) return;
        setStatus(err instanceof ApiRequestError && err.status === 403 ? "forbidden" : "error");
      },
    );
    return () => {
      live = false;
    };
  }, [tick, isInstanceAdmin]);

  return { status, updates, count: updates.filter((u) => u.update_available).length, reload };
}
