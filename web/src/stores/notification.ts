import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  Notification,
  NotificationListResponse,
  NotificationPreference,
  TelegramBotInfo,
  UpdateNotificationPreferencesRequest,
} from "@/types";

const POLL_INTERVAL_MS = 30_000;

interface NotificationState {
  notifications: Notification[];
  unreadCount: number;
  preferences: NotificationPreference[];
  emailAvailable: boolean;
  telegramBotInfo: TelegramBotInfo | null;
  isLoading: boolean;
  pollingHandle: ReturnType<typeof setInterval> | null;

  fetchNotifications: () => Promise<void>;
  markAsRead: (ids: string[]) => Promise<void>;
  markAllAsRead: () => Promise<void>;
  fetchPreferences: () => Promise<void>;
  fetchEmailAvailability: () => Promise<void>;
  fetchTelegramBotInfo: (workspaceId: string) => Promise<void>;
  updatePreferences: (
    req: UpdateNotificationPreferencesRequest,
  ) => Promise<NotificationPreference>;
  startPolling: () => void;
  stopPolling: () => void;
}

export const useNotificationStore = create<NotificationState>((set, get) => ({
  notifications: [],
  unreadCount: 0,
  preferences: [],
  emailAvailable: false,
  telegramBotInfo: null,
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
        body: { ids },
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
        body: { mark_all: true },
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

  fetchEmailAvailability: async () => {
    try {
      const data = await api<{ available: boolean }>(
        "/api/v1/notifications/email-availability",
      );
      set({ emailAvailable: data.available ?? false });
    } catch {
      // Fail closed: an instance we couldn't ask is treated the same as one
      // that answered "no" — the settings page must not invite a subscription
      // it cannot confirm will ever deliver.
      set({ emailAvailable: false });
    }
  },

  fetchTelegramBotInfo: async (workspaceId: string) => {
    try {
      const data = await api<TelegramBotInfo>(
        `/api/v1/notifications/telegram-bot-info?workspace_id=${workspaceId}`,
      );
      set({ telegramBotInfo: data });
    } catch {
      // Fail closed, same reasoning as fetchEmailAvailability: an instance we
      // couldn't ask is treated as having no bot configured.
      set({ telegramBotInfo: { available: false, bot_username: "" } });
    }
  },

  updatePreferences: async (req: UpdateNotificationPreferencesRequest) => {
    // Let failures propagate — the settings page shows them to the user.
    // Swallowing here previously masked every save failure as a silent success.
    const updated = await api<NotificationPreference>(
      "/api/v1/notifications/preferences",
      {
        method: "PUT",
        body: req,
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
    return updated;
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
