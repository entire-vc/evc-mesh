import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation } from "react-router";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Check, Save } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { LanguageSelect } from "@/components/language-select";
import { AppAccessSection } from "@/pages/connected-apps";
import { apiErrorMessage } from "@/lib/api-error";
import { cn } from "@/lib/cn";
import { useAuthStore } from "@/stores/auth";

type UsernameStatus = "idle" | "checking" | "available" | "taken" | "invalid";

function AccountCard() {
  const { t } = useTranslation("settings");
  const { user, updateProfile, checkUsername } = useAuthStore();
  const [name, setName] = useState(user?.name ?? "");
  const [username, setUsername] = useState(user?.username ?? "");
  const [status, setStatus] = useState<UsernameStatus>("idle");
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState<{ type: "success" | "error"; message: string } | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    setUsername(user?.username ?? "");
  }, [user?.username]);

  useEffect(() => () => {
    if (timerRef.current) clearTimeout(timerRef.current);
  }, []);

  const handleUsernameChange = useCallback(
    (value: string) => {
      setUsername(value);
      if (timerRef.current) clearTimeout(timerRef.current);
      if (value === "") {
        setStatus("idle");
        return;
      }
      if (!/^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$|^[a-z0-9]$/.test(value)) {
        setStatus("invalid");
        return;
      }
      setStatus("checking");
      timerRef.current = setTimeout(async () => {
        try {
          setStatus((await checkUsername(value)) ? "available" : "taken");
        } catch {
          setStatus("idle");
        }
      }, 400);
    },
    [checkUsername],
  );

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setFeedback({ type: "error", message: t("workspace.profile.nameRequired") });
      return;
    }
    if ([...trimmed].length > 100) {
      setFeedback({ type: "error", message: t("workspace.profile.nameTooLong") });
      return;
    }
    setSaving(true);
    setFeedback(null);
    try {
      await updateProfile(trimmed, username.trim() || undefined);
      setFeedback({ type: "success", message: t("workspace.profile.saved") });
    } catch (err) {
      setFeedback({ type: "error", message: apiErrorMessage(err, t("workspace.profile.saveFailed")) });
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("workspace.tabs.profile")}</CardTitle>
        <CardDescription>{t("workspace.profile.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSave} className="space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="profile-name" className="text-sm font-medium">
              {t("workspace.profile.displayName")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="profile-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("workspace.profile.namePlaceholder")}
              maxLength={100}
            />
            <p className="text-xs text-muted-foreground">
              {t("workspace.profile.currentEmail", { email: user?.email })}
            </p>
          </div>
          <div className="space-y-1.5">
            <label htmlFor="profile-username" className="text-sm font-medium">
              {t("workspace.profile.username")}
            </label>
            <Input
              id="profile-username"
              value={username}
              onChange={(e) => handleUsernameChange(e.target.value.toLowerCase())}
              placeholder={t("workspace.profile.usernamePlaceholder")}
              maxLength={40}
              className={
                status === "taken" || status === "invalid"
                  ? "border-destructive"
                  : status === "available"
                    ? "border-tone-success-500"
                    : ""
              }
            />
            {status === "checking" && (
              <p className="text-xs text-muted-foreground">{t("workspace.profile.checking")}</p>
            )}
            {status === "available" && (
              <p className="text-xs text-tone-success-600">{t("workspace.profile.usernameAvailable")}</p>
            )}
            {status === "taken" && (
              <p className="text-xs text-destructive">{t("workspace.profile.usernameTaken")}</p>
            )}
            {status === "invalid" && (
              <p className="text-xs text-destructive">{t("workspace.profile.usernameInvalid")}</p>
            )}
          </div>
          {feedback && (
            <p
              role={feedback.type === "error" ? "alert" : "status"}
              className={cn(
                "flex items-center gap-1 text-sm",
                feedback.type === "success" ? "text-tone-success-600" : "text-destructive",
              )}
            >
              {feedback.type === "success" ? (
                <Check className="h-3.5 w-3.5 shrink-0" />
              ) : (
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
              )}
              {feedback.message}
            </p>
          )}
          <Button type="submit" size="sm" disabled={saving}>
            <Save className="h-3.5 w-3.5" />
            {saving ? t("workspace.assignment.saving") : t("workspace.profile.save")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function AppearanceCard() {
  const { t } = useTranslation("settings");
  const [isDark, setIsDark] = useState(document.documentElement.classList.contains("dark"));

  const apply = (dark: boolean) => {
    setIsDark(dark);
    document.documentElement.classList.toggle("dark", dark);
    localStorage.setItem("theme", dark ? "dark" : "light");
  };

  const options = [
    { dark: false, label: t("profilePage.appearance.light") },
    { dark: true, label: t("profilePage.appearance.dark") },
  ];

  return (
    <Card>
      <CardHeader>
        <CardTitle id="appearance-title">{t("profilePage.appearance.title")}</CardTitle>
        <CardDescription>{t("profilePage.appearance.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <div role="radiogroup" aria-labelledby="appearance-title" className="flex flex-wrap gap-2">
          {options.map(({ dark, label }) => (
            <Button
              key={label}
              type="button"
              role="radio"
              aria-checked={isDark === dark}
              variant={isDark === dark ? "default" : "outline"}
              size="sm"
              onClick={() => apply(dark)}
            >
              {label}
            </Button>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function LanguageCard() {
  const { t } = useTranslation("settings");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("profilePage.language.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <LanguageSelect />
      </CardContent>
    </Card>
  );
}

// Placeholder: the list (agents the user created or supervises) is a separate
// card, since the API does not return it yet.
function MyAgentsCard() {
  const { t } = useTranslation("settings");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("profilePage.myAgents.title")}</CardTitle>
        <CardDescription>{t("profilePage.myAgents.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex items-center gap-3 text-sm text-muted-foreground">
        {t("profilePage.myAgents.placeholder")}
      </CardContent>
    </Card>
  );
}

/** Personal screen of the signed-in user, opened from the user menu. */
export function ProfilePage() {
  const { t } = useTranslation("settings");
  const { hash } = useLocation();
  // Deep link to a section (/me#app-access): the router does not scroll to it.
  useEffect(() => {
    if (hash) document.getElementById(hash.slice(1))?.scrollIntoView();
  }, [hash]);
  return (
    <div className="mx-auto w-full max-w-3xl space-y-6 p-4 sm:p-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t("profilePage.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("profilePage.description")}</p>
      </div>
      <AccountCard />
      <AppearanceCard />
      <LanguageCard />
      <MyAgentsCard />
      <AppAccessSection />
    </div>
  );
}
