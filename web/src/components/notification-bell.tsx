import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { Bell, Check, CheckCheck, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useNotificationStore } from "@/stores/notification";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Notification } from "@/types";

// ---------------------------------------------------------------------------
// NotificationBell component — header bell icon with dropdown
// ---------------------------------------------------------------------------

export function NotificationBell() {
  const navigate = useNavigate();
  const { isAuthenticated } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();
  const {
    notifications,
    unreadCount,
    isLoading,
    startPolling,
    stopPolling,
    markAsRead,
    markAllAsRead,
    fetchNotifications,
  } = useNotificationStore();

  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Start polling when user is authenticated
  useEffect(() => {
    if (!isAuthenticated) return;
    startPolling();
    return () => stopPolling();
  }, [isAuthenticated, startPolling, stopPolling]);

  // Close dropdown on outside click
  useEffect(() => {
    const handleClick = (e: MouseEvent) => {
      if (
        containerRef.current &&
        !containerRef.current.contains(e.target as Node)
      ) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, []);

  const handleToggle = useCallback(() => {
    setOpen((prev) => {
      if (!prev) {
        void fetchNotifications();
      }
      return !prev;
    });
  }, [fetchNotifications]);

  const handleNotificationClick = useCallback(
    (n: Notification) => {
      // Mark as read
      if (!n.is_read) {
        void markAsRead([n.id]);
      }
      // Navigate to task if task_id is present in metadata
      const meta = n.metadata as Record<string, unknown>;
      const taskId = meta?.task_id as string | undefined;
      const projectId = meta?.project_id as string | undefined;

      if (taskId && projectId && currentWorkspace) {
        // We only have IDs, not slugs — navigate to a search/task page.
        // Use the workspace slug from currentWorkspace and task_id directly.
        navigate(
          `/w/${currentWorkspace.slug}/t/${taskId}`,
        );
      }
      setOpen(false);
    },
    [markAsRead, navigate, currentWorkspace],
  );

  const handleMarkAll = useCallback(
    (e: React.MouseEvent) => {
      e.stopPropagation();
      void markAllAsRead();
    },
    [markAllAsRead],
  );

  const unreadItems = notifications.filter((n) => !n.is_read);

  return (
    <div ref={containerRef} className="relative">
      <Button
        variant="ghost"
        size="icon"
        onClick={handleToggle}
        aria-label={`Notifications${unreadCount > 0 ? ` (${unreadCount} unread)` : ""}`}
        className="relative"
      >
        <Bell className="h-4 w-4" />
        {unreadCount > 0 && (
          <span className="absolute -right-0.5 -top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-primary text-[10px] font-medium text-primary-foreground">
            {unreadCount > 9 ? "9+" : unreadCount}
          </span>
        )}
      </Button>

      {open && (
        <div className="absolute right-0 top-full z-50 mt-1 w-80 overflow-hidden rounded-lg border border-border bg-popover shadow-lg">
          {/* Header */}
          <div className="flex items-center justify-between border-b border-border px-3 py-2">
            <span className="text-sm font-medium">Notifications</span>
            <div className="flex items-center gap-1">
              {isLoading && (
                <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
              )}
              {unreadCount > 0 && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 gap-1 px-1.5 text-xs text-muted-foreground hover:text-foreground"
                  onClick={handleMarkAll}
                  title="Mark all as read"
                >
                  <CheckCheck className="h-3.5 w-3.5" />
                  Mark all read
                </Button>
              )}
            </div>
          </div>

          {/* Notification list */}
          <ul className="max-h-96 overflow-y-auto">
            {notifications.length === 0 ? (
              <li className="px-3 py-6 text-center text-sm text-muted-foreground">
                No notifications
              </li>
            ) : (
              notifications.map((n) => (
                <NotificationItem
                  key={n.id}
                  notification={n}
                  onMarkRead={(id) => void markAsRead([id])}
                  onClick={() => handleNotificationClick(n)}
                />
              ))
            )}
          </ul>

          {/* Footer */}
          {unreadItems.length === 0 && notifications.length > 0 && (
            <div className="border-t border-border px-3 py-2 text-center text-xs text-muted-foreground">
              All caught up
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// NotificationItem — single row in the dropdown
// ---------------------------------------------------------------------------

interface NotificationItemProps {
  notification: Notification;
  onMarkRead: (id: string) => void;
  onClick: () => void;
}

function NotificationItem({
  notification: n,
  onMarkRead,
  onClick,
}: NotificationItemProps) {
  const handleMarkRead = (e: React.MouseEvent) => {
    e.stopPropagation();
    onMarkRead(n.id);
  };

  const relativeTime = formatRelative(n.created_at);

  return (
    <li
      className={`flex cursor-pointer items-start gap-2.5 border-b border-border/50 px-3 py-2.5 text-sm transition-colors last:border-b-0 hover:bg-accent ${
        !n.is_read ? "bg-accent/30" : ""
      }`}
      onClick={onClick}
    >
      {/* Unread indicator */}
      <span
        className={`mt-1 h-1.5 w-1.5 shrink-0 rounded-full ${
          !n.is_read ? "bg-primary" : "bg-transparent"
        }`}
      />

      <div className="min-w-0 flex-1">
        <p className="truncate font-medium leading-tight">{n.title}</p>
        {n.body && (
          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
            {n.body}
          </p>
        )}
        <p className="mt-1 text-xs text-muted-foreground">{relativeTime}</p>
      </div>

      {/* Mark read button */}
      {!n.is_read && (
        <button
          className="ml-auto shrink-0 rounded p-0.5 text-muted-foreground hover:bg-background hover:text-foreground"
          onClick={handleMarkRead}
          title="Mark as read"
        >
          <Check className="h-3 w-3" />
        </button>
      )}
    </li>
  );
}

// ---------------------------------------------------------------------------
// formatRelative — simple relative time formatter
// ---------------------------------------------------------------------------

function formatRelative(isoString: string): string {
  const date = new Date(isoString);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSeconds = Math.floor(diffMs / 1000);

  if (diffSeconds < 60) return "just now";
  const diffMinutes = Math.floor(diffSeconds / 60);
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 7) return `${diffDays}d ago`;
  return date.toLocaleDateString();
}
