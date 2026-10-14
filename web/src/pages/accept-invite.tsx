import { type FormEvent, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { Eye, EyeOff } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, setAccessToken } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { apiErrorMessage } from "@/lib/api-error";

interface InviteInfo {
  id: string;
  workspace_id: string;
  email: string;
  role: string;
  expires_at: string;
}

export function AcceptInvitePage() {
  const { t } = useTranslation("misc");
  const { token } = useParams<{ token: string }>();
  const navigate = useNavigate();
  const { fetchMe } = useAuthStore();

  const [invite, setInvite] = useState<InviteInfo | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);

  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    api<InviteInfo>(`/api/v1/invites/${token}`)
      .then((data) => {
        setInvite(data);
        setIsLoading(false);
      })
      .catch(() => {
        setLoadError(t("invite.accept.invalid"));
        setIsLoading(false);
      });
  }, [token, t]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    // Required, not optional. When it was optional the server fell back to
    // storing the address in display_name, and nothing in the product could
    // correct it afterwards — which is how an instance ends up showing every
    // member as their own address on every task and comment.
    if (!name.trim()) {
      setError(t("invite.accept.nameRequired"));
      return;
    }
    if (!password) {
      setError(t("invite.accept.passwordRequired"));
      return;
    }
    if (password !== confirmPassword) {
      setError(t("invite.accept.passwordMismatch"));
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      const resp = await api<{ access_token: string }>(
        `/api/v1/invites/${token}/accept`,
        { method: "POST", body: { name: name.trim(), password }, noAuth: true, withCredentials: true },
      );
      setAccessToken(resp.access_token);
      await fetchMe();
      navigate("/", { replace: true });
    } catch (err) {
      setError(apiErrorMessage(err, t("invite.accept.failed")));
    } finally {
      setIsSubmitting(false);
    }
  };

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <p className="text-muted-foreground">{t("invite.accept.loading")}</p>
      </div>
    );
  }

  if (loadError || !invite) {
    return (
      <div className="flex min-h-screen items-center justify-center p-4">
        <div className="w-full max-w-md text-center space-y-4">
          <h1 className="text-2xl font-bold">{t("invite.accept.notFoundTitle")}</h1>
          <p className="text-muted-foreground">{loadError ?? t("invite.accept.noLongerValid")}</p>
          <Button variant="outline" onClick={() => navigate("/login")}>
            {t("invite.accept.goToLogin")}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <div className="w-full max-w-md space-y-6">
        <div className="text-center space-y-1">
          <h1 className="text-2xl font-bold">{t("invite.accept.title")}</h1>
          <p className="text-muted-foreground">
            <Trans t={t} i18nKey="invite.accept.invitedAs" values={{ role: invite.role }} components={{ b: <strong /> }} />
          </p>
          <p className="text-sm text-muted-foreground">
            <Trans t={t} i18nKey="invite.accept.signingInAs" values={{ email: invite.email }} components={{ b: <span className="font-medium text-foreground" /> }} />
          </p>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="ai-name" className="text-sm font-medium">
              {t("invite.accept.nameLabel")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="ai-name"
              type="text"
              placeholder={t("invite.accept.namePlaceholder")}
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              maxLength={100}
            />
            <p className="text-xs text-muted-foreground">
              {t("invite.accept.nameHint")}
            </p>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="ai-password" className="text-sm font-medium">
              {t("invite.accept.passwordLabel")} <span className="text-destructive">*</span>
            </label>
            <div className="relative">
              <Input
                id="ai-password"
                type={showPassword ? "text" : "password"}
                placeholder={t("invite.accept.passwordPlaceholder")}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="pr-9"
                autoComplete="new-password"
              />
              <button
                type="button"
                className="absolute inset-y-0 right-3 flex items-center text-muted-foreground hover:text-foreground"
                onClick={() => setShowPassword((v) => !v)}
                tabIndex={-1}
              >
                {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="ai-confirm" className="text-sm font-medium">
              {t("invite.accept.confirmLabel")} <span className="text-destructive">*</span>
            </label>
            <Input
              id="ai-confirm"
              type={showPassword ? "text" : "password"}
              placeholder={t("invite.accept.confirmPlaceholder")}
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              autoComplete="new-password"
            />
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}

          <Button type="submit" className="w-full" disabled={isSubmitting}>
            {isSubmitting ? t("invite.accept.submitting") : t("invite.accept.submit")}
          </Button>
        </form>
      </div>
    </div>
  );
}
