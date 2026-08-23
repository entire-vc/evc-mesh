import { useEffect, useState } from "react";
import { useParams, useSearchParams } from "react-router";
import { Bell } from "lucide-react";
import { subscribeUser, unsubscribeUser, isSubscribed, getPermissionState } from "@/lib/push";
import { toast } from "@/components/ui/toast";
import { useNotificationStore } from "@/stores/notification";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import type { TelegramPreferenceConfig } from "@/types";
import { NotificationSettingsTabs } from "./tabs";
import { InAppTab } from "./in-app-tab";
import { BrowserPushTab } from "./browser-push-tab";
import { EmailTab } from "./email-tab";
import { TelegramTab } from "./telegram-tab";
import type { TabId } from "./constants";
import { apiErrorMessage } from "@/lib/api-error";

const VALID_TABS: TabId[] = ["in-app", "push", "email", "telegram"];

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

  const [searchParams, setSearchParams] = useSearchParams();
  const tabParam = searchParams.get("tab");
  const activeTab: TabId =
    tabParam && (VALID_TABS as string[]).includes(tabParam)
      ? (tabParam as TabId)
      : "in-app";
  const setActiveTab = (tab: TabId) => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.set("tab", tab);
        return next;
      },
      { replace: true },
    );
  };

  const [selectedEvents, setSelectedEvents] = useState<Set<string>>(
    new Set([
      "task.assigned", "task.status_changed", "comment.created", "task.mentioned",
      "document.mentioned", "task.blocking_triage", "task.reviewer_assigned", "task.ready_for_review",
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
      'task.assigned', 'task.status_changed', 'comment.created', 'task.mentioned',
      'document.mentioned', 'task.blocking_triage', 'task.reviewer_assigned', 'task.ready_for_review',
    ]),
  );
  const [pushEventsSaving, setPushEventsSaving] = useState(false);
  const [pushEventsSaved, setPushEventsSaved] = useState(false);

  const [emailEnabled, setEmailEnabled] = useState(true);
  const [emailEvents, setEmailEvents] = useState<Set<string>>(
    new Set([
      "task.assigned", "task.status_changed", "comment.created", "task.mentioned",
      "document.mentioned", "task.blocking_triage", "task.reviewer_assigned", "task.ready_for_review",
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
      "task.assigned", "task.status_changed", "comment.created", "task.mentioned",
      "document.mentioned", "task.blocking_triage", "task.reviewer_assigned", "task.ready_for_review",
    ]),
  );
  const [telegramUsername, setTelegramUsername] = useState("");
  const [telegramChatID, setTelegramChatID] = useState<number | null>(null);
  const [telegramBindLink, setTelegramBindLink] = useState<string | null>(null);
  const [telegramSaving, setTelegramSaving] = useState(false);
  const [telegramSaved, setTelegramSaved] = useState(false);
  const [telegramUnbinding, setTelegramUnbinding] = useState(false);

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
      toast.error(apiErrorMessage(err, "Failed to enable browser push"));
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
      toast.error(apiErrorMessage(err, "Failed to disable browser push"));
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
        apiErrorMessage(err, "Failed to save push preferences"),
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
        apiErrorMessage(err, "Failed to save notification settings"),
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

  const handleEmailAddressChange = (value: string) => {
    setEmailAddress(value);
    setEmailAddressInitialized(true);
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
        apiErrorMessage(err, "Failed to save email notification settings"),
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

  // Save and Disconnect are the same PUT; the only difference is that
  // disconnecting turns the channel off.
  //
  // That is the whole mechanism: the server carries chat_id forward only while
  // is_enabled is true, so saving a disabled row drops the binding and any
  // outstanding bind token with it. Disconnect therefore needs no API of its
  // own — it is the disable path with a name, which is the part that was
  // actually missing. Nothing on this page told a user that turning the toggle
  // off and saving is how you stop the bot messaging you, so in practice
  // nobody could.
  const submitTelegram = async (unbind: boolean) => {
    if (!wsID) return;
    const trimmed = telegramUsername.trim();
    const nextEnabled = unbind ? false : telegramEnabled;
    if (nextEnabled && !trimmed) {
      toast.error("Enter your Telegram username");
      return;
    }

    if (unbind) setTelegramUnbinding(true);
    else setTelegramSaving(true);
    try {
      const updated = await updatePreferences({
        workspace_id: wsID,
        channel: "telegram",
        events: Array.from(telegramEvents),
        is_enabled: nextEnabled,
        config: { telegram_username: trimmed },
      });
      const cfg = updated.config as TelegramPreferenceConfig | undefined;
      setTelegramChatID(cfg?.telegram_chat_id ?? null);
      setTelegramBindLink(
        cfg?.telegram_bind_token && telegramBotInfo?.bot_username
          ? `https://t.me/${telegramBotInfo.bot_username}?start=${cfg.telegram_bind_token}`
          : null,
      );
      if (unbind) {
        setTelegramEnabled(false);
        toast.success("Telegram disconnected — the bot can no longer message you");
      } else {
        setTelegramSaved(true);
        setTimeout(() => setTelegramSaved(false), 2000);
      }
    } catch (err) {
      toast.error(
        apiErrorMessage(
          err,
          unbind ? "Failed to disconnect Telegram" : "Failed to save Telegram notification settings",
        ),
      );
    } finally {
      if (unbind) setTelegramUnbinding(false);
      else setTelegramSaving(false);
    }
  };

  const handleSaveTelegram = () => submitTelegram(false);
  const handleDisconnectTelegram = () => submitTelegram(true);

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

      <NotificationSettingsTabs
        activeTab={activeTab}
        onChange={setActiveTab}
        emailUnavailable={isLoaded && !emailAvailable}
        telegramUnavailable={!!telegramBotInfo && !telegramBotInfo.available}
      />

      {activeTab === "in-app" && (
        <InAppTab
          isLoaded={isLoaded}
          isEnabled={isEnabled}
          setIsEnabled={setIsEnabled}
          selectedEvents={selectedEvents}
          toggleEvent={toggleEvent}
          isSaving={isSaving}
          saved={saved}
          onSave={() => void handleSave()}
        />
      )}

      {activeTab === "push" && (
        <BrowserPushTab
          pushPermission={pushPermission}
          pushSubscribed={pushSubscribed}
          pushLoading={pushLoading}
          onEnablePush={() => void handleEnablePush()}
          onDisablePush={() => void handleDisablePush()}
          pushEvents={pushEvents}
          togglePushEvent={togglePushEvent}
          pushEventsSaving={pushEventsSaving}
          pushEventsSaved={pushEventsSaved}
          onSavePushEvents={() => void handleSavePushEvents()}
        />
      )}

      {activeTab === "email" && (
        <EmailTab
          isLoaded={isLoaded}
          emailAvailable={emailAvailable}
          emailEnabled={emailEnabled}
          setEmailEnabled={setEmailEnabled}
          emailAddress={emailAddress}
          onEmailAddressChange={handleEmailAddressChange}
          user={user}
          emailEvents={emailEvents}
          toggleEmailEvent={toggleEmailEvent}
          emailSaving={emailSaving}
          emailSaved={emailSaved}
          onSave={() => void handleSaveEmail()}
        />
      )}

      {activeTab === "telegram" && (
        <TelegramTab
          telegramBotInfo={telegramBotInfo}
          telegramEnabled={telegramEnabled}
          setTelegramEnabled={setTelegramEnabled}
          telegramUsername={telegramUsername}
          onTelegramUsernameChange={setTelegramUsername}
          telegramChatID={telegramChatID}
          telegramBindLink={telegramBindLink}
          telegramEvents={telegramEvents}
          toggleTelegramEvent={toggleTelegramEvent}
          telegramSaving={telegramSaving}
          telegramSaved={telegramSaved}
          telegramUnbinding={telegramUnbinding}
          onSave={() => void handleSaveTelegram()}
          onDisconnect={() => void handleDisconnectTelegram()}
        />
      )}
    </div>
  );
}
