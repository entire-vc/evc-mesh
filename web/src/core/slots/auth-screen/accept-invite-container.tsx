import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { Trans, useTranslation } from "react-i18next";
import { api, setAccessToken } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { apiErrorMessage } from "@/lib/api-error";
import { AuthScreenSlot, useSetValue } from "./use-auth-slot";
import type { AuthScreenData } from "./types";

interface InviteInfo {
  id: string;
  workspace_id: string;
  email: string;
  role: string;
  expires_at: string;
}

export function AcceptInviteContainer() {
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

  const submit = useCallback(async () => {
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
      const resp = await api<{ access_token: string }>(`/api/v1/invites/${token}/accept`, {
        method: "POST",
        body: { name: name.trim(), password },
        noAuth: true,
        withCredentials: true,
      });
      setAccessToken(resp.access_token);
      await fetchMe();
      navigate("/", { replace: true });
    } catch (err) {
      setError(apiErrorMessage(err, t("invite.accept.failed")));
    } finally {
      setIsSubmitting(false);
    }
  }, [name, password, confirmPassword, token, fetchMe, navigate, t]);

  const setValue = useSetValue({
    name: setName,
    password: setPassword,
    confirm_password: setConfirmPassword,
  });

  const base = {
    mode: "accept-invite" as const,
    fields: [],
    submitLabel: "",
    submitting: false,
    formError: null,
    registrationEnabled: false,
    links: { login: "/login", register: null, forgot: null },
    components: {},
  };

  let data: AuthScreenData;
  if (isLoading) {
    data = {
      ...base,
      status: "loading",
      heading: { title: t("invite.accept.loading") },
      footer: null,
    };
  } else if (loadError || !invite) {
    data = {
      ...base,
      status: "invalid",
      heading: {
        title: t("invite.accept.notFoundTitle"),
        subtitle: loadError ?? t("invite.accept.noLongerValid"),
      },
      footer: { linkLabel: t("invite.accept.goToLogin"), to: "/login" },
    };
  } else {
    data = {
      ...base,
      status: "form",
      heading: {
        title: t("invite.accept.title"),
        subtitle: (
          <Trans
            t={t}
            i18nKey="invite.accept.invitedAs"
            values={{ role: invite.role }}
            components={{ b: <strong /> }}
          />
        ),
        detail: (
          <Trans
            t={t}
            i18nKey="invite.accept.signingInAs"
            values={{ email: invite.email }}
            components={{ b: <span className="font-medium text-foreground" /> }}
          />
        ),
      },
      fields: [
        {
          name: "name",
          type: "text",
          label: t("invite.accept.nameLabel"),
          placeholder: t("invite.accept.namePlaceholder"),
          hint: t("invite.accept.nameHint"),
          required: true,
          autoComplete: "", // the former page set none
          autoFocus: true,
          maxLength: 100,
          value: name,
        },
        {
          name: "password",
          type: "password",
          label: t("invite.accept.passwordLabel"),
          placeholder: t("invite.accept.passwordPlaceholder"),
          required: true,
          revealable: true,
          autoComplete: "new-password",
          value: password,
        },
        {
          name: "confirm_password",
          type: "password",
          label: t("invite.accept.confirmLabel"),
          placeholder: t("invite.accept.confirmPlaceholder"),
          required: true,
          autoComplete: "new-password",
          value: confirmPassword,
        },
      ],
      submitLabel: isSubmitting ? t("invite.accept.submitting") : t("invite.accept.submit"),
      submitting: isSubmitting,
      formError: error,
      footer: null,
    };
  }

  return <AuthScreenSlot data={data} onSetValue={setValue} onSubmit={submit} onNavigate={navigate} />;
}
