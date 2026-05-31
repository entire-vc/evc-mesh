import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";

interface TeamRelayIntegration {
  enabled: boolean;
}

export function useProjectTrIntegration(projId: string | undefined) {
  const [enabled, setEnabled] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (!projId) {
      setEnabled(false);
      return;
    }
    setIsLoading(true);
    api<TeamRelayIntegration>(`/api/v1/projects/${projId}/integrations/team-relay`)
      .then((data) => {
        if (mountedRef.current) setEnabled(data.enabled);
      })
      .catch(() => {
        if (mountedRef.current) setEnabled(false);
      })
      .finally(() => {
        if (mountedRef.current) setIsLoading(false);
      });
  }, [projId]);

  return { enabled, isLoading };
}
