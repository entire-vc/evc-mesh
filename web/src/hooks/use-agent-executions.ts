import { useCallback, useEffect, useRef, useState } from "react";
import { fetchAgentExecutions, type ExecutionLoad } from "@/lib/agent-executions";

/**
 * One scoped read of the agent-executions projection. A slower response for a
 * previous filter must not overwrite a newer one, so each request is numbered.
 */
export function useAgentExecutions(
  workspaceId: string | null | undefined,
  filter: { projectId?: string | null; agentId?: string | null },
): { load: ExecutionLoad | "loading"; reload: () => void } {
  const [load, setLoad] = useState<ExecutionLoad | "loading">("loading");
  const requestId = useRef(0);
  const { projectId, agentId } = filter;

  const reload = useCallback(() => {
    const id = ++requestId.current;
    if (!workspaceId) {
      setLoad({ kind: "error" });
      return;
    }
    setLoad("loading");
    void fetchAgentExecutions(workspaceId, { projectId, agentId }).then((result) => {
      if (requestId.current === id) setLoad(result);
    });
  }, [workspaceId, projectId, agentId]);

  useEffect(() => {
    reload();
    return () => {
      requestId.current++;
    };
  }, [reload]);

  return { load, reload };
}
