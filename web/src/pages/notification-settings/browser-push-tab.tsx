import { AlertTriangle, Check, Monitor, MonitorOff, Save } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";

const PUSH_EVENTS = [
  { key: "task.assigned", label: "Task assigned", desc: "When a task is assigned to you" },
  { key: "task.status_changed", label: "Status changed", desc: "When a task status changes" },
  { key: "comment.created", label: "New comment", desc: "When a comment is added" },
  { key: "task.mentioned", label: "Mention", desc: "When someone @mentions you" },
  { key: "task.blocking_triage", label: "Blocking triage", desc: "When a task you blocked is auto-moved to triage" },
  { key: "task.reviewer_assigned", label: "Review requested", desc: "When you're set as the reviewer on a task" },
  { key: "task.ready_for_review", label: "Ready for review", desc: "When a task you're reviewing moves to a review status" },
];

interface BrowserPushTabProps {
  pushPermission: NotificationPermission;
  pushSubscribed: boolean;
  pushLoading: boolean;
  onEnablePush: () => void;
  onDisablePush: () => void;
  pushEvents: Set<string>;
  togglePushEvent: (key: string) => void;
  pushEventsSaving: boolean;
  pushEventsSaved: boolean;
  onSavePushEvents: () => void;
}

export function BrowserPushTab({
  pushPermission,
  pushSubscribed,
  pushLoading,
  onEnablePush,
  onDisablePush,
  pushEvents,
  togglePushEvent,
  pushEventsSaving,
  pushEventsSaved,
  onSavePushEvents,
}: BrowserPushTabProps) {
  return (
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
              onClick={onDisablePush}
              disabled={pushLoading}
            >
              <MonitorOff className="mr-1 h-4 w-4" />
              Disable
            </Button>
          ) : (
            <Button size="sm" onClick={onEnablePush} disabled={pushLoading}>
              <Monitor className="mr-1 h-4 w-4" />
              Enable
            </Button>
          )}
        </div>

        {/* Per-event toggles (only shown when subscribed) */}
        {pushSubscribed && (
          <>
            <div className="space-y-2">
              {PUSH_EVENTS.map((evt) => (
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
                onClick={onSavePushEvents}
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
  );
}
