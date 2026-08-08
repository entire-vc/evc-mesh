import { useEffect, useState } from "react";
import { useParams } from "react-router";
import { Bell, Check, Save, Monitor, MonitorOff, AlertTriangle } from "lucide-react";
import { subscribeUser, unsubscribeUser, isSubscribed, getPermissionState } from "@/lib/push";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { useNotificationStore } from "@/stores/notification";
import { useWorkspaceStore } from "@/stores/workspace";

// ---------------------------------------------------------------------------
// Event definitions shown in the settings UI
// ---------------------------------------------------------------------------

interface EventConfig {
  key: string;
  label: string;
  description: string;
}

const NOTIFICATION_EVENTS: EventConfig[] = [
  {
    key: "task.assigned",
    label: "Task assigned",
    description: "When a task is assigned to you or someone in your workspace",
  },
  {
    key: "task.status_changed",
    label: "Status changed",
    description: "When a task's status changes",
  },
  {
    key: "comment.created",
    label: "New comment",
    description: "When a comment is added to a task",
  },
  {
    key: "task.blocking_triage",
    label: "Blocking triage",
    description: "When a task you're mentioned in is auto-moved to triage as a blocker",
  },
  {
    key: "task.reviewer_assigned",
    label: "Review requested",
    description: "When you're set as the reviewer on a task",
  },
  {
    key: "task.ready_for_review",
    label: "Ready for review",
    description: "When a task you're reviewing moves to a review status",
  },
];

// ---------------------------------------------------------------------------
// NotificationSettings page
// ---------------------------------------------------------------------------

export default function NotificationSettingsPage() {
  const { wsId } = useParams();
  const { currentWorkspace } = useWorkspaceStore();
  const { preferences, fetchPreferences, updatePreferences } =
    useNotificationStore();

  const [selectedEvents, setSelectedEvents] = useState<Set<string>>(
    new Set([
      "task.assigned", "task.status_changed", "comment.created", "task.blocking_triage",
      "task.reviewer_assigned", "task.ready_for_review",
    ]),
  );
  const [isEnabled, setIsEnabled] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [isLoaded, setIsLoaded] = useState(false);

  const [pushPermission, setPushPermission] = useState<NotificationPermission>('default');
  const [pushSubscribed, setPushSubscribed] = useState(false);
  const [pushLoading, setPushLoading] = useState(false);
  const [pushEvents, setPushEvents] = useState<Set<string>>(
    new Set([
      'task.assigned', 'task.status_changed', 'comment.created', 'task.mentioned', 'task.blocking_triage',
      'task.reviewer_assigned', 'task.ready_for_review',
    ]),
  );
  const [pushEventsSaving, setPushEventsSaving] = useState(false);
  const [pushEventsSaved, setPushEventsSaved] = useState(false);

  useEffect(() => {
    void (async () => {
      setPushPermission(await getPermissionState());
      setPushSubscribed(await isSubscribed());
    })();
  }, []);

  const handleEnablePush = async () => {
    setPushLoading(true);
    try {
      await subscribeUser();
      setPushPermission(await getPermissionState());
      setPushSubscribed(true);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to enable browser push");
    } finally {
      setPushLoading(false);
    }
  };

  const handleDisablePush = async () => {
    setPushLoading(true);
    try {
      await unsubscribeUser();
      setPushSubscribed(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to disable browser push");
    } finally {
      setPushLoading(false);
    }
  };

  const togglePushEvent = (key: string) => {
    setPushEvents((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  };

  const handleSavePushEvents = async () => {
    const wsID = currentWorkspace?.id ?? wsId;
    if (!wsID) return;
    setPushEventsSaving(true);
    try {
      await updatePreferences({
        workspace_id: wsID,
        channel: 'browser_push',
        events: Array.from(pushEvents),
        is_enabled: pushSubscribed,
      });
      setPushEventsSaved(true);
      setTimeout(() => setPushEventsSaved(false), 2000);
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save push preferences",
      );
    } finally {
      setPushEventsSaving(false);
    }
  };

  // Load preferences on mount
  useEffect(() => {
    void fetchPreferences().then(() => setIsLoaded(true));
  }, [fetchPreferences]);

  // Sync preferences into local state when loaded
  useEffect(() => {
    if (!isLoaded || preferences.length === 0) return;
    const webPushPref = preferences.find((p) => p.channel === "web_push");
    if (webPushPref) {
      setSelectedEvents(new Set(webPushPref.events));
      setIsEnabled(webPushPref.is_enabled);
    }
  }, [isLoaded, preferences]);

  const toggleEvent = (key: string) => {
    setSelectedEvents((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  const handleSave = async () => {
    const wsID = currentWorkspace?.id ?? wsId;
    if (!wsID) return;

    setIsSaving(true);
    try {
      await updatePreferences({
        workspace_id: wsID,
        channel: "web_push",
        events: Array.from(selectedEvents),
        is_enabled: isEnabled,
      });
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save notification settings",
      );
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <div className="flex items-center gap-3">
        <Bell className="h-6 w-6 text-primary" />
        <div>
          <h1 className="text-xl font-semibold">Notification Settings</h1>
          <p className="text-sm text-muted-foreground">
            Configure which events send you in-app notifications.
          </p>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">In-App Notifications</CardTitle>
          <CardDescription>
            Choose which events you want to be notified about. Notifications
            appear in the bell icon in the header.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Master toggle */}
          <div className="flex items-center justify-between rounded-lg border border-border p-3">
            <div>
              <p className="font-medium">Enable notifications</p>
              <p className="text-sm text-muted-foreground">
                Receive in-app notifications for workspace activity
              </p>
            </div>
            <button
              role="switch"
              aria-checked={isEnabled}
              onClick={() => setIsEnabled((prev) => !prev)}
              className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                isEnabled ? "bg-primary" : "bg-muted-foreground/30"
              }`}
            >
              <span
                className={`inline-block h-4 w-4 rounded-full bg-white shadow transition-transform ${
                  isEnabled ? "translate-x-4" : "translate-x-0.5"
                }`}
              />
            </button>
          </div>

          {/* Per-event toggles */}
          {!isLoaded ? (
            <div className="space-y-2">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : (
            <div
              className={`space-y-2 transition-opacity ${isEnabled ? "opacity-100" : "opacity-40 pointer-events-none"}`}
            >
              {NOTIFICATION_EVENTS.map((evt) => (
                <div
                  key={evt.key}
                  className="flex items-center justify-between rounded-lg border border-border p-3 transition-colors hover:bg-accent/30"
                >
                  <div>
                    <p className="font-medium">{evt.label}</p>
                    <p className="text-sm text-muted-foreground">
                      {evt.description}
                    </p>
                  </div>
                  <button
                    role="switch"
                    aria-checked={selectedEvents.has(evt.key)}
                    onClick={() => toggleEvent(evt.key)}
                    disabled={!isEnabled}
                    className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                      selectedEvents.has(evt.key)
                        ? "bg-primary"
                        : "bg-muted-foreground/30"
                    }`}
                  >
                    <span
                      className={`inline-block h-4 w-4 rounded-full bg-white shadow transition-transform ${
                        selectedEvents.has(evt.key)
                          ? "translate-x-4"
                          : "translate-x-0.5"
                      }`}
                    />
                  </button>
                </div>
              ))}
            </div>
          )}

          {/* Save button */}
          <div className="flex justify-end pt-2">
            <Button
              onClick={() => void handleSave()}
              disabled={isSaving}
              className="gap-2"
            >
              {saved ? (
                <>
                  <Check className="h-4 w-4" />
                  Saved
                </>
              ) : (
                <>
                  <Save className="h-4 w-4" />
                  {isSaving ? "Saving..." : "Save preferences"}
                </>
              )}
            </Button>
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <Monitor className="h-4 w-4" />
            Browser Push Notifications
          </CardTitle>
          <CardDescription>
            Receive native browser notifications even when Mesh is not in the
            foreground.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Permission status */}
          <div className="flex items-center justify-between rounded-lg border border-border p-3">
            <div>
              <p className="font-medium">Status</p>
              <p className="text-sm text-muted-foreground">
                {pushPermission === 'denied'
                  ? 'Blocked — re-enable in browser settings'
                  : pushSubscribed
                  ? 'Enabled'
                  : 'Not enabled'}
              </p>
            </div>
            {pushPermission === 'denied' ? (
              <AlertTriangle className="h-5 w-5 text-destructive" />
            ) : pushSubscribed ? (
              <Button
                variant="outline"
                size="sm"
                onClick={() => void handleDisablePush()}
                disabled={pushLoading}
              >
                <MonitorOff className="mr-1 h-4 w-4" />
                Disable
              </Button>
            ) : (
              <Button
                size="sm"
                onClick={() => void handleEnablePush()}
                disabled={pushLoading}
              >
                <Monitor className="mr-1 h-4 w-4" />
                Enable
              </Button>
            )}
          </div>

          {/* Per-event toggles (only shown when subscribed) */}
          {pushSubscribed && (
            <>
              <div
                className="space-y-2"
              >
                {[
                  { key: 'task.assigned', label: 'Task assigned', desc: 'When a task is assigned to you' },
                  { key: 'task.status_changed', label: 'Status changed', desc: 'When a task status changes' },
                  { key: 'comment.created', label: 'New comment', desc: 'When a comment is added' },
                  { key: 'task.mentioned', label: 'Mention', desc: 'When someone @mentions you' },
                  { key: 'task.blocking_triage', label: 'Blocking triage', desc: 'When a task you blocked is auto-moved to triage' },
                  { key: 'task.reviewer_assigned', label: 'Review requested', desc: 'When you\'re set as the reviewer on a task' },
                  { key: 'task.ready_for_review', label: 'Ready for review', desc: 'When a task you\'re reviewing moves to a review status' },
                ].map((evt) => (
                  <div
                    key={evt.key}
                    className="flex items-center justify-between rounded-lg border border-border p-3 transition-colors hover:bg-accent/30"
                  >
                    <div>
                      <p className="font-medium">{evt.label}</p>
                      <p className="text-sm text-muted-foreground">{evt.desc}</p>
                    </div>
                    <button
                      role="switch"
                      aria-checked={pushEvents.has(evt.key)}
                      onClick={() => togglePushEvent(evt.key)}
                      className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                        pushEvents.has(evt.key) ? 'bg-primary' : 'bg-muted-foreground/30'
                      }`}
                    >
                      <span
                        className={`inline-block h-4 w-4 rounded-full bg-white shadow transition-transform ${
                          pushEvents.has(evt.key) ? 'translate-x-4' : 'translate-x-0.5'
                        }`}
                      />
                    </button>
                  </div>
                ))}
              </div>
              <div className="flex justify-end pt-2">
                <Button
                  onClick={() => void handleSavePushEvents()}
                  disabled={pushEventsSaving}
                  className="gap-2"
                >
                  {pushEventsSaved ? (
                    <><Check className="h-4 w-4" />Saved</>
                  ) : (
                    <><Save className="h-4 w-4" />{pushEventsSaving ? 'Saving...' : 'Save preferences'}</>
                  )}
                </Button>
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
