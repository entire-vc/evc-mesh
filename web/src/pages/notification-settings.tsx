import { useEffect, useState } from "react";
import { useParams } from "react-router";
import { Bell, Check, Save, Monitor, MonitorOff, AlertTriangle, Mail, Send, ExternalLink } from "lucide-react";
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
import { useAuthStore } from "@/stores/auth";
import type { TelegramPreferenceConfig } from "@/types";

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
  const {
    preferences,
    emailAvailable,
    telegramBotInfo,
    fetchPreferences,
    fetchEmailAvailability,
    fetchTelegramBotInfo,
    updatePreferences,
  } = useNotificationStore();
  const { user } = useAuthStore();
  const wsID = currentWorkspace?.id ?? wsId;

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

  const [emailEnabled, setEmailEnabled] = useState(true);
  const [emailEvents, setEmailEvents] = useState<Set<string>>(
    new Set([
      "task.assigned", "task.status_changed", "comment.created", "task.blocking_triage",
      "task.reviewer_assigned", "task.ready_for_review",
    ]),
  );
  const [emailAddress, setEmailAddress] = useState("");
  // Distinguishes "nothing loaded yet" from "user cleared the field on
  // purpose" — without it, the account-email fallback below would stomp on an
  // edit the user is still typing every time preferences/user re-render.
  const [emailAddressInitialized, setEmailAddressInitialized] = useState(false);
  const [emailSaving, setEmailSaving] = useState(false);
  const [emailSaved, setEmailSaved] = useState(false);

  const [telegramEnabled, setTelegramEnabled] = useState(true);
  const [telegramEvents, setTelegramEvents] = useState<Set<string>>(
    new Set([
      "task.assigned", "task.status_changed", "comment.created", "task.blocking_triage",
      "task.reviewer_assigned", "task.ready_for_review",
    ]),
  );
  const [telegramUsername, setTelegramUsername] = useState("");
  const [telegramChatID, setTelegramChatID] = useState<number | null>(null);
  const [telegramBindLink, setTelegramBindLink] = useState<string | null>(null);
  const [telegramSaving, setTelegramSaving] = useState(false);
  const [telegramSaved, setTelegramSaved] = useState(false);

  useEffect(() => {
    void (async () => {
      setPushPermission(await getPermissionState());
      setPushSubscribed(await isSubscribed());
    })();
    void fetchEmailAvailability();
  }, [fetchEmailAvailability]);

  useEffect(() => {
    if (wsID) void fetchTelegramBotInfo(wsID);
  }, [wsID, fetchTelegramBotInfo]);

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
    if (!isLoaded || preferences.length === 0 || !wsID) return;
    const webPushPref = preferences.find(
      (p) => p.channel === "web_push" && p.workspace_id === wsID,
    );
    if (webPushPref) {
      setSelectedEvents(new Set(webPushPref.events));
      setIsEnabled(webPushPref.is_enabled);
    }
    // Event selection is a standing preference, saved and restored on its own —
    // it must not depend on whether the browser happens to be subscribed right
    // now (that comes from isSubscribed() separately, above).
    const browserPushPref = preferences.find(
      (p) => p.channel === "browser_push" && p.workspace_id === wsID,
    );
    if (browserPushPref) {
      setPushEvents(new Set(browserPushPref.events));
    }
    const emailPref = preferences.find(
      (p) => p.channel === "email" && p.workspace_id === wsID,
    );
    if (emailPref) {
      setEmailEvents(new Set(emailPref.events));
      setEmailEnabled(emailPref.is_enabled);
      const savedAddress = emailPref.config?.email;
      if (typeof savedAddress === "string" && savedAddress) {
        setEmailAddress(savedAddress);
        setEmailAddressInitialized(true);
      }
    }
    const telegramPref = preferences.find(
      (p) => p.channel === "telegram" && p.workspace_id === wsID,
    );
    if (telegramPref) {
      setTelegramEvents(new Set(telegramPref.events));
      setTelegramEnabled(telegramPref.is_enabled);
      const cfg = telegramPref.config as TelegramPreferenceConfig | undefined;
      setTelegramUsername(cfg?.telegram_username ?? "");
      setTelegramChatID(cfg?.telegram_chat_id ?? null);
      setTelegramBindLink(
        cfg?.telegram_bind_token && telegramBotInfo?.bot_username
          ? `https://t.me/${telegramBotInfo.bot_username}?start=${cfg.telegram_bind_token}`
          : null,
      );
    }
  }, [isLoaded, preferences, wsID, telegramBotInfo]);

  // The address field defaults to the account email until a saved custom
  // address (above) or a manual edit (the input's own onChange) claims it —
  // this only ever fires once, before either of those has had a chance to.
  useEffect(() => {
    if (emailAddressInitialized || !user?.email) return;
    setEmailAddress(user.email);
    setEmailAddressInitialized(true);
  }, [emailAddressInitialized, user?.email]);

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

  const toggleEmailEvent = (key: string) => {
    setEmailEvents((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  };

  const handleSaveEmail = async () => {
    if (!wsID) return;
    const trimmed = emailAddress.trim();
    if (!trimmed) {
      toast.error("Enter an email address");
      return;
    }

    setEmailSaving(true);
    try {
      await updatePreferences({
        workspace_id: wsID,
        channel: "email",
        events: Array.from(emailEvents),
        is_enabled: emailEnabled,
        config: { email: trimmed },
      });
      setEmailSaved(true);
      setTimeout(() => setEmailSaved(false), 2000);
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save email notification settings",
      );
    } finally {
      setEmailSaving(false);
    }
  };

  const toggleTelegramEvent = (key: string) => {
    setTelegramEvents((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  };

  const handleSaveTelegram = async () => {
    if (!wsID) return;
    const trimmed = telegramUsername.trim();
    if (telegramEnabled && !trimmed) {
      toast.error("Enter your Telegram username");
      return;
    }

    setTelegramSaving(true);
    try {
      const updated = await updatePreferences({
        workspace_id: wsID,
        channel: "telegram",
        events: Array.from(telegramEvents),
        is_enabled: telegramEnabled,
        config: { telegram_username: trimmed },
      });
      const cfg = updated.config as TelegramPreferenceConfig | undefined;
      setTelegramChatID(cfg?.telegram_chat_id ?? null);
      setTelegramBindLink(
        cfg?.telegram_bind_token && telegramBotInfo?.bot_username
          ? `https://t.me/${telegramBotInfo.bot_username}?start=${cfg.telegram_bind_token}`
          : null,
      );
      setTelegramSaved(true);
      setTimeout(() => setTelegramSaved(false), 2000);
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save Telegram notification settings",
      );
    } finally {
      setTelegramSaving(false);
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
      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <Mail className="h-4 w-4" />
            Email Notifications
          </CardTitle>
          <CardDescription>
            Receive notifications by email, at the account address or a custom
            one you set below.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {!isLoaded ? (
            <Skeleton className="h-16 w-full" />
          ) : !emailAvailable ? (
            <div className="flex items-start gap-3 rounded-lg border border-border p-3">
              <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-destructive" />
              <div>
                <p className="font-medium">Email notifications are not available</p>
                <p className="text-sm text-muted-foreground">
                  This Mesh instance has no outbound email server configured. Ask
                  your administrator to set one up to enable this channel.
                </p>
              </div>
            </div>
          ) : (
            <>
              {/* Master toggle */}
              <div className="flex items-center justify-between rounded-lg border border-border p-3">
                <div>
                  <p className="font-medium">Enable email notifications</p>
                  <p className="text-sm text-muted-foreground">
                    Receive an email for the events selected below
                  </p>
                </div>
                <button
                  role="switch"
                  aria-checked={emailEnabled}
                  onClick={() => setEmailEnabled((prev) => !prev)}
                  className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                    emailEnabled ? "bg-primary" : "bg-muted-foreground/30"
                  }`}
                >
                  <span
                    className={`inline-block h-4 w-4 rounded-full bg-white shadow transition-transform ${
                      emailEnabled ? "translate-x-4" : "translate-x-0.5"
                    }`}
                  />
                </button>
              </div>

              {/* Delivery address */}
              <div className="space-y-1.5 rounded-lg border border-border p-3">
                <label htmlFor="email-address" className="font-medium">
                  Email address
                </label>
                <p className="text-sm text-muted-foreground">
                  Defaults to your account email. Change it to deliver
                  notifications somewhere else.
                </p>
                <input
                  id="email-address"
                  type="email"
                  value={emailAddress}
                  onChange={(e) => {
                    setEmailAddress(e.target.value);
                    setEmailAddressInitialized(true);
                  }}
                  placeholder={user?.email ?? "you@example.com"}
                  className="mt-1 w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                />
              </div>

              {/* Per-event toggles */}
              <div
                className={`space-y-2 transition-opacity ${emailEnabled ? "opacity-100" : "opacity-40 pointer-events-none"}`}
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
                      aria-checked={emailEvents.has(evt.key)}
                      onClick={() => toggleEmailEvent(evt.key)}
                      disabled={!emailEnabled}
                      className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                        emailEvents.has(evt.key)
                          ? "bg-primary"
                          : "bg-muted-foreground/30"
                      }`}
                    >
                      <span
                        className={`inline-block h-4 w-4 rounded-full bg-white shadow transition-transform ${
                          emailEvents.has(evt.key)
                            ? "translate-x-4"
                            : "translate-x-0.5"
                        }`}
                      />
                    </button>
                  </div>
                ))}
              </div>

              <div className="flex justify-end pt-2">
                <Button
                  onClick={() => void handleSaveEmail()}
                  disabled={emailSaving}
                  className="gap-2"
                >
                  {emailSaved ? (
                    <><Check className="h-4 w-4" />Saved</>
                  ) : (
                    <><Save className="h-4 w-4" />{emailSaving ? "Saving..." : "Save preferences"}</>
                  )}
                </Button>
              </div>
            </>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <Send className="h-4 w-4" />
            Telegram Notifications
          </CardTitle>
          <CardDescription>
            Receive notifications from a Telegram bot connected to this
            workspace.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {!telegramBotInfo ? (
            <Skeleton className="h-16 w-full" />
          ) : !telegramBotInfo.available ? (
            <div className="flex items-start gap-3 rounded-lg border border-border p-3">
              <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-destructive" />
              <div>
                <p className="font-medium">Telegram is not connected</p>
                <p className="text-sm text-muted-foreground">
                  Ask a workspace owner or admin to connect a bot from the{" "}
                  <span className="font-medium">Integrations</span> page.
                </p>
              </div>
            </div>
          ) : (
            <>
              {/* Master toggle */}
              <div className="flex items-center justify-between rounded-lg border border-border p-3">
                <div>
                  <p className="font-medium">Enable Telegram notifications</p>
                  <p className="text-sm text-muted-foreground">
                    Receive a message for the events selected below
                  </p>
                </div>
                <button
                  role="switch"
                  aria-checked={telegramEnabled}
                  onClick={() => setTelegramEnabled((prev) => !prev)}
                  className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                    telegramEnabled ? "bg-primary" : "bg-muted-foreground/30"
                  }`}
                >
                  <span
                    className={`inline-block h-4 w-4 rounded-full bg-white shadow transition-transform ${
                      telegramEnabled ? "translate-x-4" : "translate-x-0.5"
                    }`}
                  />
                </button>
              </div>

              {/* Username + connection status */}
              <div className="space-y-1.5 rounded-lg border border-border p-3">
                <label htmlFor="telegram-username" className="font-medium">
                  Telegram username
                </label>
                <p className="text-sm text-muted-foreground">
                  Save your username, then open the bot below to finish
                  connecting.
                </p>
                <input
                  id="telegram-username"
                  type="text"
                  value={telegramUsername}
                  onChange={(e) => setTelegramUsername(e.target.value)}
                  placeholder="your_telegram_username"
                  className="mt-1 w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                />
                {telegramChatID ? (
                  <p className="flex items-center gap-1.5 pt-1 text-sm text-teal-600">
                    <Check className="h-4 w-4" />
                    Connected — @{telegramBotInfo.bot_username} can message you
                  </p>
                ) : telegramBindLink ? (
                  <a
                    href={telegramBindLink}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1.5 pt-1 text-sm font-medium text-primary hover:underline"
                  >
                    Open @{telegramBotInfo.bot_username} to finish connecting
                    <ExternalLink className="h-3.5 w-3.5" />
                  </a>
                ) : (
                  <p className="pt-1 text-sm text-muted-foreground">
                    Save with the toggle above enabled to get a connection link.
                  </p>
                )}
              </div>

              {/* Per-event toggles */}
              <div
                className={`space-y-2 transition-opacity ${telegramEnabled ? "opacity-100" : "opacity-40 pointer-events-none"}`}
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
                      aria-checked={telegramEvents.has(evt.key)}
                      onClick={() => toggleTelegramEvent(evt.key)}
                      disabled={!telegramEnabled}
                      className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                        telegramEvents.has(evt.key)
                          ? "bg-primary"
                          : "bg-muted-foreground/30"
                      }`}
                    >
                      <span
                        className={`inline-block h-4 w-4 rounded-full bg-white shadow transition-transform ${
                          telegramEvents.has(evt.key)
                            ? "translate-x-4"
                            : "translate-x-0.5"
                        }`}
                      />
                    </button>
                  </div>
                ))}
              </div>

              <div className="flex justify-end pt-2">
                <Button
                  onClick={() => void handleSaveTelegram()}
                  disabled={telegramSaving}
                  className="gap-2"
                >
                  {telegramSaved ? (
                    <><Check className="h-4 w-4" />Saved</>
                  ) : (
                    <><Save className="h-4 w-4" />{telegramSaving ? "Saving..." : "Save preferences"}</>
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
