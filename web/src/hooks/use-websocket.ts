import { useEffect, useRef } from "react";
import { useWebSocketStore } from "@/stores/websocket";
import type { WSMessage } from "@/types";

interface UseWebSocketOptions {
  /** Workspace slug to connect to. */
  workspaceSlug: string | undefined;
  /** Optional project ID to subscribe to project-level events. */
  projectId?: string | undefined;
  /** Callback invoked for each incoming WebSocket event. */
  onEvent?: (event: WSMessage) => void;
}

/**
 * Hook that manages a WebSocket connection to the EVC Mesh event stream.
 *
 * Automatically connects to the workspace channel and optionally subscribes
 * to a project-specific channel. Handles reconnection and cleanup.
 */
export function useWebSocket({
  workspaceSlug,
  projectId,
  onEvent,
}: UseWebSocketOptions) {
  const { isConnected, lastEvent, connect, disconnect, subscribe, unsubscribe } =
    useWebSocketStore();

  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  // Connect to workspace.
  useEffect(() => {
    if (workspaceSlug) {
      connect(workspaceSlug);
    }

    return () => {
      // Don't disconnect on unmount -- the connection is global.
      // It will be disconnected when the user logs out or
      // when the workspace changes.
    };
  }, [workspaceSlug, connect]);

  // Subscribe to project channel.
  useEffect(() => {
    if (!projectId) return;

    const channel = `project:${projectId}`;
    subscribe(channel);

    return () => {
      unsubscribe(channel);
    };
  }, [projectId, subscribe, unsubscribe]);

  // Invoke onEvent callback when a new event arrives.
  useEffect(() => {
    if (lastEvent && onEventRef.current) {
      onEventRef.current(lastEvent);
    }
  }, [lastEvent]);

  return {
    isConnected,
    lastEvent,
    subscribe,
    unsubscribe,
    disconnect,
  };
}
