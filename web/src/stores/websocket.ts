import { create } from "zustand";
import { getAccessToken } from "@/lib/api";
import type { WSMessage } from "@/types";

const BASE_URL = import.meta.env.VITE_API_URL || "";

// Exponential backoff parameters.
const INITIAL_RECONNECT_DELAY = 1000;
const MAX_RECONNECT_DELAY = 30000;
const RECONNECT_BACKOFF_MULTIPLIER = 2;

interface WSState {
  isConnected: boolean;
  lastEvent: WSMessage | null;
  eventLog: WSMessage[];

  connect: (workspaceSlug: string) => void;
  disconnect: () => void;
  subscribe: (channel: string) => void;
  unsubscribe: (channel: string) => void;
}

let socket: WebSocket | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let reconnectDelay = INITIAL_RECONNECT_DELAY;
let currentWorkspaceSlug: string | null = null;
let pendingSubscriptions: Set<string> = new Set();
let intentionalClose = false;

// Maximum number of events to keep in the event log.
const MAX_EVENT_LOG = 200;

export const useWebSocketStore = create<WSState>((set, get) => ({
  isConnected: false,
  lastEvent: null,
  eventLog: [],

  connect: (workspaceSlug: string) => {
    // If already connected to this workspace, skip.
    if (
      currentWorkspaceSlug === workspaceSlug &&
      socket &&
      socket.readyState === WebSocket.OPEN
    ) {
      return;
    }

    // Disconnect any existing connection.
    get().disconnect();

    currentWorkspaceSlug = workspaceSlug;
    intentionalClose = false;

    doConnect(workspaceSlug, set, get);
  },

  disconnect: () => {
    intentionalClose = true;
    currentWorkspaceSlug = null;
    pendingSubscriptions.clear();

    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }

    if (socket) {
      socket.close();
      socket = null;
    }

    set({ isConnected: false });
  },

  subscribe: (channel: string) => {
    pendingSubscriptions.add(channel);

    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ action: "subscribe", channel }));
    }
  },

  unsubscribe: (channel: string) => {
    pendingSubscriptions.delete(channel);

    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ action: "unsubscribe", channel }));
    }
  },
}));

type SetState = (
  partial:
    | Partial<WSState>
    | ((state: WSState) => Partial<WSState>),
) => void;

function doConnect(
  workspaceSlug: string,
  set: SetState,
  get: () => WSState,
) {
  const token = getAccessToken();
  if (!token) {
    return;
  }

  // Build WebSocket URL.
  const wsProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  let wsBase: string;

  if (BASE_URL) {
    // Replace http(s) with ws(s).
    wsBase = BASE_URL.replace(/^http/, "ws");
  } else {
    wsBase = `${wsProtocol}//${window.location.host}`;
  }

  const url = `${wsBase}/ws?token=${encodeURIComponent(token)}&workspace=${encodeURIComponent(workspaceSlug)}`;

  try {
    socket = new WebSocket(url);
  } catch {
    scheduleReconnect(workspaceSlug, set, get);
    return;
  }

  socket.onopen = () => {
    reconnectDelay = INITIAL_RECONNECT_DELAY;
    set({ isConnected: true });

    // Re-subscribe to all pending channels.
    for (const channel of pendingSubscriptions) {
      socket?.send(JSON.stringify({ action: "subscribe", channel }));
    }
  };

  socket.onmessage = (event: MessageEvent) => {
    try {
      const msg = JSON.parse(event.data as string) as WSMessage;
      set((state) => ({
        lastEvent: msg,
        eventLog: [msg, ...state.eventLog].slice(0, MAX_EVENT_LOG),
      }));
    } catch {
      // Ignore unparseable messages.
    }
  };

  socket.onclose = () => {
    socket = null;
    set({ isConnected: false });

    if (!intentionalClose && currentWorkspaceSlug === workspaceSlug) {
      scheduleReconnect(workspaceSlug, set, get);
    }
  };

  socket.onerror = () => {
    // onerror is always followed by onclose, so reconnection is handled there.
  };
}

function scheduleReconnect(
  workspaceSlug: string,
  set: SetState,
  get: () => WSState,
) {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
  }

  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    if (currentWorkspaceSlug === workspaceSlug && !intentionalClose) {
      doConnect(workspaceSlug, set, get);
    }
  }, reconnectDelay);

  // Exponential backoff.
  reconnectDelay = Math.min(
    reconnectDelay * RECONNECT_BACKOFF_MULTIPLIER,
    MAX_RECONNECT_DELAY,
  );
}
