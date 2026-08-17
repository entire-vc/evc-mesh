import { Check, Save } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { NOTIFICATION_EVENTS } from "./constants";

interface InAppTabProps {
  isLoaded: boolean;
  isEnabled: boolean;
  setIsEnabled: (updater: (prev: boolean) => boolean) => void;
  selectedEvents: Set<string>;
  toggleEvent: (key: string) => void;
  isSaving: boolean;
  saved: boolean;
  onSave: () => void;
}

export function InAppTab({
  isLoaded,
  isEnabled,
  setIsEnabled,
  selectedEvents,
  toggleEvent,
  isSaving,
  saved,
  onSave,
}: InAppTabProps) {
  return (
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
          <Button onClick={onSave} disabled={isSaving} className="gap-2">
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
  );
}
