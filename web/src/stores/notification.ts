import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  Notification,
  NotificationListResponse,
  NotificationPreference,
  UpdateNotificationPreferencesRequest,
} from "@/types";

const POLL_INTERVAL_MS = 30_000;

interface NotificationState {
  notifications: Notification[];
  unreadCount: number;
  preferences: NotificationPreference[];
  isLoading: boolean;
  pollingHandle: ReturnType<typeof setInterval> | null;

  fetchNotifications: () => Promise<void>;
  markAsRead: (ids: string[]) => Promise<void>;
  markAllAsRead: () => Promise<void>;
  fetchPreferences: () => Promise<void>;
  updatePreferences: (
    req: UpdateNotificationPreferencesRequest,
  ) => Promise<void>;
  startPolling: () => void;
  stopPolling: () => void;
}

export const useNotificationStore = create<NotificationState>((set, get) => ({
  notifications: [],
  unreadCount: 0,
  preferences: [],
  isLoading: false,
  pollingHandle: null,

  fetchNotifications: async () => {
    set({ isLoading: true });
    try {
      const data = await api<NotificationListResponse>(
        "/api/v1/notifications",
      );
      set({
        notifications: data.items ?? [],
        unreadCount: data.unread_count ?? 0,
        isLoading: false,
      });
    } catch {
      set({ isLoading: false });
    }
  },

  markAsRead: async (ids: string[]) => {
    if (ids.length === 0) return;
    try {
      await api("/api/v1/notifications/mark-read", {
        method: "POST",
        body: JSON.stringify({ ids }),
      });
      set((state) => ({
        notifications: state.notifications.map((n) =>
          ids.includes(n.id) ? { ...n, is_read: true } : n,
        ),
        unreadCount: Math.max(
          0,
          state.unreadCount -
            state.notifications.filter(
              (n) => ids.includes(n.id) && !n.is_read,
            ).length,
        ),
      }));
    } catch {
      // Silently ignore mark-read failures
    }
  },

  markAllAsRead: async () => {
    try {
      await api("/api/v1/notifications/mark-read", {
        method: "POST",
        body: JSON.stringify({ mark_all: true }),
      });
      set((state) => ({
        notifications: state.notifications.map((n) => ({
          ...n,
          is_read: true,
        })),
        unreadCount: 0,
      }));
    } catch {
      // Silently ignore mark-read failures
    }
  },

  fetchPreferences: async () => {
    try {
      const data = await api<{ preferences: NotificationPreference[] }>(
        "/api/v1/notifications/preferences",
      );
      set({ preferences: data.preferences ?? [] });
    } catch {
      // Silently ignore preference fetch failures
    }
  },

  updatePreferences: async (req: UpdateNotificationPreferencesRequest) => {
    try {
      const updated = await api<NotificationPreference>(
        "/api/v1/notifications/preferences",
        {
          method: "PUT",
          body: JSON.stringify(req),
        },
      );
      set((state) => {
        const existing = state.preferences.findIndex(
          (p) => p.id === updated.id,
        );
        if (existing >= 0) {
          const next = [...state.preferences];
          next[existing] = updated;
          return { preferences: next };
        }
        return { preferences: [...state.preferences, updated] };
      });
    } catch {
      // Silently ignore preference update failures
    }
  },

  startPolling: () => {
    const { pollingHandle, fetchNotifications } = get();
    if (pollingHandle !== null) return; // already polling

    // Fetch immediately, then set up interval
    void fetchNotifications();
    const handle = setInterval(() => {
      void fetchNotifications();
    }, POLL_INTERVAL_MS);

    set({ pollingHandle: handle });
  },

  stopPolling: () => {
    const { pollingHandle } = get();
    if (pollingHandle !== null) {
      clearInterval(pollingHandle);
      set({ pollingHandle: null });
    }
  },
}));
