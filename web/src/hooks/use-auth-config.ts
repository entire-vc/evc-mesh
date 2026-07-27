import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { AuthConfig } from "@/types";

export interface UseAuthConfigReturn {
  // Whether new users may currently self-register. Defaults to true while the
  // request is in flight or if it fails — fail-open on the UI so a transient
  // network hiccup never hides the register link on an instance that is
  // actually open. The backend is the real gate either way (POST
  // /auth/register enforces this independently of what the link shows).
  registrationEnabled: boolean;
  loading: boolean;
}

export function useAuthConfig(): UseAuthConfigReturn {
  const [registrationEnabled, setRegistrationEnabled] = useState(true);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    api<AuthConfig>("/api/v1/auth/config", { noAuth: true })
      .then((config) => {
        if (!cancelled) setRegistrationEnabled(config.registration_enabled);
      })
      .catch(() => {
        // Fail-open (see registrationEnabled doc above).
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { registrationEnabled, loading };
}
