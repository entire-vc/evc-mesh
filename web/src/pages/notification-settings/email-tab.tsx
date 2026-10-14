import { AlertTriangle, Check, Mail, Save } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { User } from "@/types";
import { useTranslation } from "react-i18next";
import { NOTIFICATION_EVENTS } from "./constants";

interface EmailTabProps {
  isLoaded: boolean;
  emailAvailable: boolean;
  emailEnabled: boolean;
  setEmailEnabled: (updater: (prev: boolean) => boolean) => void;
  emailAddress: string;
  onEmailAddressChange: (value: string) => void;
  user: User | null;
  emailEvents: Set<string>;
  toggleEmailEvent: (key: string) => void;
  emailSaving: boolean;
  emailSaved: boolean;
  onSave: () => void;
}

export function EmailTab({
  isLoaded,
  emailAvailable,
  emailEnabled,
  setEmailEnabled,
  emailAddress,
  onEmailAddressChange,
  user,
  emailEvents,
  toggleEmailEvent,
  emailSaving,
  emailSaved,
  onSave,
}: EmailTabProps) {
  const { t } = useTranslation("notifications");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base flex items-center gap-2">
          <Mail className="h-4 w-4" />
          {t("settings.email.title")}
        </CardTitle>
        <CardDescription>
          {t("settings.email.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {!isLoaded ? (
          <Skeleton className="h-16 w-full" />
        ) : !emailAvailable ? (
          <div className="flex items-start gap-3 rounded-lg border border-border p-3">
            <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-destructive" />
            <div>
              <p className="font-medium">{t("settings.email.unavailableTitle")}</p>
              <p className="text-sm text-muted-foreground">
                {t("settings.email.unavailableHint")}
              </p>
            </div>
          </div>
        ) : (
          <>
            {/* Master toggle */}
            <div className="flex items-center justify-between rounded-lg border border-border p-3">
              <div>
                <p className="font-medium">{t("settings.email.enable")}</p>
                <p className="text-sm text-muted-foreground">
                  {t("settings.email.enableHint")}
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
                {t("settings.email.address")}
              </label>
              <p className="text-sm text-muted-foreground">
                {t("settings.email.addressHint")}
              </p>
              <input
                id="email-address"
                type="email"
                value={emailAddress}
                onChange={(e) => onEmailAddressChange(e.target.value)}
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
                    <p className="font-medium">{t(`settings.events.${evt.id}.label`)}</p>
                    <p className="text-sm text-muted-foreground">
                      {t(`settings.events.${evt.id}.description`)}
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
              <Button onClick={onSave} disabled={emailSaving} className="gap-2">
                {emailSaved ? (
                  <><Check className="h-4 w-4" />{t("settings.actions.saved")}</>
                ) : (
                  <><Save className="h-4 w-4" />{emailSaving ? t("settings.actions.saving") : t("settings.actions.save")}</>
                )}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
