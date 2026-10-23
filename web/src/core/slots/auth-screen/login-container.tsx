import { useCallback, useEffect, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import { useAuthStore } from "@/stores/auth";
import { useAuthConfig } from "@/hooks/use-auth-config";
import { ApiRequestError } from "@/lib/api";
import { clearStaticShellDraft, peekStaticShellDraft } from "@/lib/static-shell";
import { prefetchNextRouteOnIdle } from "@/lib/prefetch-next-route";
import { AuthScreenSlot, useSetValue } from "./use-auth-slot";
import type { AuthScreenData } from "./types";

export function LoginContainer() {
  // No Suspense here: /login is painted by the static shell in index.html and
  // must not drop to a skeleton while the `misc` chunk loads. Until it arrives
  // t() returns the English default, which is exactly what the shell shows.
  const { t } = useTranslation("misc", { useSuspense: false });
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { isAuthenticated, login } = useAuthStore();
  const { registrationEnabled } = useAuthConfig();
  // Seeded from the static shell in index.html, so input typed before the
  // bundle loaded survives the hand-off to React.
  const [email, setEmail] = useState(() => peekStaticShellDraft()?.email ?? "");
  const [password, setPassword] = useState(() => peekStaticShellDraft()?.password ?? "");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    const focusedId = peekStaticShellDraft()?.focusedId;
    clearStaticShellDraft();
    if (!focusedId) return;
    const el = document.getElementById(focusedId);
    if (el instanceof HTMLInputElement) {
      el.focus();
      // type=email doesn't support selection APIs; the caret lands at the end
      // on focus there anyway.
      try {
        el.setSelectionRange(el.value.length, el.value.length);
      } catch {
        /* not a text-selectable input type */
      }
    }
  }, []);

  const submit = useCallback(async () => {
    setError(null);
    setLoading(true);
    try {
      await login({ email, password });
      prefetchNextRouteOnIdle();
      const redirect = searchParams.get("redirect");
      navigate(redirect && redirect.startsWith("/") ? redirect : "/");
    } catch (err) {
      if (err instanceof ApiRequestError) {
        setError(err.message);
      } else {
        setError(t("errors.unexpected", "An unexpected error occurred"));
      }
    } finally {
      setLoading(false);
    }
  }, [email, password, login, navigate, searchParams, t]);

  const setValue = useSetValue({ email: setEmail, password: setPassword });

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  const data: AuthScreenData = {
    mode: "login",
    status: "form",
    heading: {
      title: t("auth.login.title", "Welcome back"),
      subtitle: t("auth.login.subtitle", "Sign in to your EVC Mesh account"),
    },
    fields: [
      {
        name: "email",
        type: "email",
        label: t("auth.email", "Email"),
        placeholder: t("auth.emailPlaceholder", "you@example.com"),
        required: true,
        autoComplete: "email",
        value: email,
      },
      {
        name: "password",
        type: "password",
        label: t("auth.password", "Password"),
        placeholder: t("auth.login.passwordPlaceholder", "Your password"),
        required: true,
        autoComplete: "current-password",
        value: password,
      },
    ],
    submitLabel: loading
      ? t("auth.login.submitting", "Signing in...")
      : t("auth.login.submit", "Sign in"),
    submitting: loading,
    formError: error,
    registrationEnabled,
    links: { login: "/login", register: registrationEnabled ? "/register" : null, forgot: null },
    footer: registrationEnabled
      ? {
          text: t("auth.login.noAccount", "Don't have an account?"),
          linkLabel: t("auth.login.register", "Register"),
          to: "/register",
        }
      : null,
    components: {},
  };

  return <AuthScreenSlot data={data} onSetValue={setValue} onSubmit={submit} onNavigate={navigate} />;
}
