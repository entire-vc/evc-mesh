import { AlertTriangle, Check, ExternalLink, Save, Send } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { TelegramBotInfo } from "@/types";
import { NOTIFICATION_EVENTS } from "./constants";

interface TelegramTabProps {
  telegramBotInfo: TelegramBotInfo | null;
  telegramEnabled: boolean;
  setTelegramEnabled: (updater: (prev: boolean) => boolean) => void;
  telegramUsername: string;
  onTelegramUsernameChange: (value: string) => void;
  telegramChatID: number | null;
  telegramBindLink: string | null;
  telegramEvents: Set<string>;
  toggleTelegramEvent: (key: string) => void;
  telegramSaving: boolean;
  telegramSaved: boolean;
  onSave: () => void;
}

export function TelegramTab({
  telegramBotInfo,
  telegramEnabled,
  setTelegramEnabled,
  telegramUsername,
  onTelegramUsernameChange,
  telegramChatID,
  telegramBindLink,
  telegramEvents,
  toggleTelegramEvent,
  telegramSaving,
  telegramSaved,
  onSave,
}: TelegramTabProps) {
  return (
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
                onChange={(e) => onTelegramUsernameChange(e.target.value)}
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
              <Button onClick={onSave} disabled={telegramSaving} className="gap-2">
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
  );
}
