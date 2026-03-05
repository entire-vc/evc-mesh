import { useEffect, useState } from "react";
import { useParams } from "react-router";
import { Bell, Check, Save } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
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
    new Set(["task.assigned", "task.status_changed", "comment.created"]),
  );
  const [isEnabled, setIsEnabled] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [isLoaded, setIsLoaded] = useState(false);

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
    </div>
  );
}
