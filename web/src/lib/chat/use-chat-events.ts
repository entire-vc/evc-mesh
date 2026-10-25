import { useEffect, useRef } from "react";
import { useWebSocketStore } from "@/stores/websocket";
import type { ChatEvent } from "./types";

/**
 * Feeds every chat.* event of the signed-in user to `onEvent`.
 *
 * Subscribed to the store rather than read from `lastEvent` in render: React
 * batches renders, so two events in one tick would show only the later one and
 * a delta would be lost.
 */
export function useChatEvents(userId: string | undefined, onEvent: (e: ChatEvent) => void) {
  const handler = useRef(onEvent);
  handler.current = onEvent;

  useEffect(() => {
    if (!userId) return;
    const channel = `ws:user:${userId}`;
    useWebSocketStore.getState().subscribe(channel);
    const off = useWebSocketStore.subscribe((state, prev) => {
      const msg = state.lastEvent;
      if (!msg || msg === prev.lastEvent || msg.channel !== channel) return;
      const data = msg.data as { event?: unknown };
      if (typeof data.event === "string" && data.event.startsWith("chat.")) {
        handler.current(msg.data as unknown as ChatEvent);
      }
    });
    return () => {
      off();
      useWebSocketStore.getState().unsubscribe(channel);
    };
  }, [userId]);
}
