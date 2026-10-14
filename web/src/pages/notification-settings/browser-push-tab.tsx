import { AlertTriangle, Check, Monitor, MonitorOff, Save } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { useTranslation } from "react-i18next";
import { NOTIFICATION_EVENTS } from "./constants";

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
  const { t } = useTranslation("notifications");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base flex items-center gap-2">
          <Monitor className="h-4 w-4" />
          {t("settings.push.title")}
        </CardTitle>
        <CardDescription>
          {t("settings.push.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* Permission status */}
        <div className="flex items-center justify-between rounded-lg border border-border p-3">
          <div>
            <p className="font-medium">{t("settings.push.status")}</p>
            <p className="text-sm text-muted-foreground">
              {pushPermission === 'denied'
                ? t('settings.push.blocked')
                : pushSubscribed
                ? t('settings.push.enabled')
                : t('settings.push.notEnabled')}
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
              {t("settings.push.disable")}
            </Button>
          ) : (
            <Button size="sm" onClick={onEnablePush} disabled={pushLoading}>
              <Monitor className="mr-1 h-4 w-4" />
              {t("settings.push.enable")}
            </Button>
          )}
        </div>

        {/* Per-event toggles (only shown when subscribed) */}
        {pushSubscribed && (
          <>
            <div className="space-y-2">
              {NOTIFICATION_EVENTS.map((evt) => (
                <div
                  key={evt.key}
                  className="flex items-center justify-between rounded-lg border border-border p-3 transition-colors hover:bg-accent/30"
                >
                  <div>
                    <p className="font-medium">{t(`settings.events.${evt.id}.label`)}</p>
                    <p className="text-sm text-muted-foreground">{t(`settings.events.${evt.id}.pushDescription`)}</p>
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
                  <><Check className="h-4 w-4" />{t("settings.actions.saved")}</>
                ) : (
                  <><Save className="h-4 w-4" />{pushEventsSaving ? t('settings.actions.saving') : t('settings.actions.save')}</>
                )}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
