import { useCallback } from "react";
import { useParams } from "react-router";
import { useWebSocket } from "@/hooks/use-websocket";
import type { WSMessage } from "@/types";
import type { Id, RealtimeEvent } from "./types";

export type RealtimeTopic = { kind: "project"; id: Id | null | undefined } | { kind: "workspace" };

/** Subscribes to the topic for the component's lifetime; unsubscribes on unmount. */
export function useRealtime(
  topic: RealtimeTopic,
  onEvent: (e: RealtimeEvent) => void,
): { connected: boolean } {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const handle = useCallback(
    (m: WSMessage) => onEvent({ type: m.type, data: m.data, at: m.timestamp }),
    [onEvent],
  );
  const { isConnected } = useWebSocket({
    workspaceSlug: wsSlug,
    projectId: topic.kind === "project" ? (topic.id ?? undefined) : undefined,
    onEvent: handle,
  });
  return { connected: isConnected };
}
