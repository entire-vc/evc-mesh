import { useCallback, useState } from "react";
import { Navigate, useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import { useAuthStore } from "@/stores/auth";
import { useAuthConfig } from "@/hooks/use-auth-config";
import { ApiRequestError } from "@/lib/api";
import { AuthScreenSlot, useSetValue } from "./use-auth-slot";
import type { AuthScreenData } from "./types";

export function RegisterContainer() {
  // Same as LoginContainer: rendered outside AppLayout, so no Suspense on the
  // `misc` chunk; the English default shows until it has loaded.
  const { t } = useTranslation("misc", { useSuspense: false });
  const navigate = useNavigate();
  const { isAuthenticated, register } = useAuthStore();
  const { registrationEnabled, loading: configLoading } = useAuthConfig();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const submit = useCallback(async () => {
    setError(null);
    setLoading(true);
    try {
      await register({ email, password, name });
      navigate("/");
    } catch (err) {
      if (err instanceof ApiRequestError) {
        setError(err.message);
      } else {
        setError(t("errors.unexpected", "An unexpected error occurred"));
      }
    } finally {
      setLoading(false);
    }
  }, [email, password, name, register, navigate, t]);

  const setValue = useSetValue({ name: setName, email: setEmail, password: setPassword });

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  const closed = !configLoading && !registrationEnabled;
  const data: AuthScreenData = closed
    ? {
        mode: "register",
        status: "closed",
        heading: {
          title: t("auth.register.closedTitle", "Registration is closed"),
          subtitle: t(
            "auth.register.closedBody",
            "This instance is not accepting new signups. Ask an admin for an invite, or sign in if you already have an account.",
          ),
        },
        fields: [],
        submitLabel: "",
        submitting: false,
        formError: null,
        registrationEnabled: false,
        links: { login: "/login", register: null, forgot: null },
        footer: { linkLabel: t("auth.register.backToSignIn", "Back to sign in"), to: "/login" },
        components: {},
      }
    : {
        mode: "register",
        status: "form",
        heading: {
          title: t("auth.register.title", "Create an account"),
          subtitle: t("auth.register.subtitle", "Get started with EVC Mesh"),
        },
        fields: [
          {
            name: "name",
            type: "text",
            label: t("auth.name", "Name"),
            placeholder: t("auth.namePlaceholder", "Your name"),
            required: true,
            autoComplete: "name",
            value: name,
          },
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
            placeholder: t("auth.register.passwordPlaceholder", "At least 8 characters"),
            required: true,
            minLength: 8,
            autoComplete: "new-password",
            value: password,
          },
        ],
        submitLabel: loading
          ? t("auth.register.submitting", "Creating account...")
          : t("auth.register.submit", "Create account"),
        submitting: loading,
        formError: error,
        registrationEnabled,
        links: { login: "/login", register: "/register", forgot: null },
        footer: {
          text: t("auth.register.haveAccount", "Already have an account?"),
          linkLabel: t("auth.register.signIn", "Sign in"),
          to: "/login",
        },
        components: {},
      };

  return <AuthScreenSlot data={data} onSetValue={setValue} onSubmit={submit} onNavigate={navigate} />;
}
