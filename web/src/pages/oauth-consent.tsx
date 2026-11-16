import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import i18n from "@/i18n";
import { Navigate, useLocation, useSearchParams } from "react-router";
import { AlertTriangle, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import type { OAuthConsentInfo } from "@/types";

// The authorize request the client sent, round-tripped verbatim: GET
// /oauth/authorize validates it and redirects here with the same query string,
// and POST /api/v1/oauth/consent re-validates every field, so nothing here is
// trusted just because it came back from the browser.
const AUTHORIZE_PARAMS = [
  "client_id",
  "redirect_uri",
  "response_type",
  "code_challenge",
  "code_challenge_method",
  "scope",
  "state",
] as const;

// OpenID Connect nonce: optional, so it rides along only when the client sent
// one. The server echoes it into the id_token; an empty one would be a value.
const OPTIONAL_AUTHORIZE_PARAMS = ["nonce"] as const;

type AuthorizeQuery = Record<(typeof AUTHORIZE_PARAMS)[number], string> &
  Partial<Record<(typeof OPTIONAL_AUTHORIZE_PARAMS)[number], string>>;

// Sent instead of a workspace on "Deny": the server binds workspace_id to a
// UUID and rejects "" outright, but never reads it when allow is false.
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

/** The server only hands back a redirect it already validated against the
 *  client's registered URIs (https, or http on loopback). This is the belt to
 *  that braces, and an allowlist rather than a denylist: anything the server
 *  never issues (javascript:, data:, blob:, about:, custom schemes) is refused
 *  here instead of being enumerated. */
export function isSafeRedirect(target: string): boolean {
  try {
    const { protocol } = new URL(target);
    return protocol === "http:" || protocol === "https:";
  } catch {
    return false;
  }
}

// Bidi controls and zero-width/format characters let a name or host render as
// something it is not. DCR accepts any client_name that has no ASCII control
// characters, so strip these before the name goes on a trust decision.
// eslint-disable-next-line no-misleading-character-class
const INVISIBLE_CHARS = /[\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF]/g;

/** A 401 that means "your session is gone", as opposed to a 401 the OAuth
 *  server sends for a bad request (an unknown client_id answers 401
 *  invalid_client). api() marks the first kind itself: it only throws
 *  code=UNAUTHORIZED after its refresh attempt failed. Treating every 401 as an
 *  expired session sent a signed-in user with a mistyped client_id to /login
 *  instead of an error page. */
export function isSessionExpired(err: unknown): boolean {
  const e = err as { status?: number; code?: string } | null;
  return e?.status === 401 && e?.code === "UNAUTHORIZED";
}

export function displayClientName(name: string): string {
  return name.replace(INVISIBLE_CHARS, "").trim() || i18n.t("integrations:oauthConsent.unnamedApp");
}

/** The host the browser will actually be sent to, read from the URL itself.
 *  URL.host is punycode, so a Cyrillic look-alike of a familiar name shows as
 *  xn--… rather than passing for the real one. Falls back to the server's
 *  string only if the URL cannot be parsed. */
export function redirectHost(redirectUri: string, fallback: string): string {
  try {
    return new URL(redirectUri).host;
  } catch {
    return fallback;
  }
}

/** Loopback by the URI actually being used. The server's loopback_warning is
 *  true only when EVERY registered URI is loopback, so a client that registers
 *  one https and one loopback URI and presents the loopback one would get no
 *  warning from it alone. */
export function isLoopbackRedirect(redirectUri: string): boolean {
  try {
    const host = new URL(redirectUri).hostname.replace(/^\[|\]$/g, "");
    return host === "localhost" || host === "::1" || /^127(\.\d{1,3}){3}$/.test(host);
  } catch {
    return false;
  }
}

type Phase =
  | { kind: "loading" }
  | { kind: "error"; message: string; retryable: boolean }
  | { kind: "ready"; info: OAuthConsentInfo }
  | { kind: "redirecting" };

export function OAuthConsentPage() {
  const { t } = useTranslation("integrations");
  const { isAuthenticated, isLoading: authLoading, user } = useAuthStore();
  const location = useLocation();
  const [searchParams] = useSearchParams();

  const query = {} as AuthorizeQuery;
  for (const key of AUTHORIZE_PARAMS) query[key] = searchParams.get(key) ?? "";
  for (const key of OPTIONAL_AUTHORIZE_PARAMS) {
    const value = searchParams.get(key);
    if (value !== null) query[key] = value;
  }
  const queryKey = [...AUTHORIZE_PARAMS, ...OPTIONAL_AUTHORIZE_PARAMS]
    .map((k) => query[k] ?? "")
    .join("\n");

  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [workspaceId, setWorkspaceId] = useState("");
  const [submitting, setSubmitting] = useState<"allow" | "deny" | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);

  // A page restored from the back/forward cache keeps its old state. After a
  // decision that state is "Returning you to the app…" with no way out, so a
  // restored page starts over instead.
  useEffect(() => {
    const onShow = (e: PageTransitionEvent) => {
      if (e.persisted) window.location.reload();
    };
    window.addEventListener("pageshow", onShow);
    return () => window.removeEventListener("pageshow", onShow);
  }, []);

  const toLogin = () => {
    window.location.assign(
      `/login?redirect=${encodeURIComponent(`${location.pathname}${location.search}`)}`,
    );
  };

  useEffect(() => {
    if (!isAuthenticated) return;
    let cancelled = false;
    setPhase({ kind: "loading" });
    api<OAuthConsentInfo>("/api/v1/oauth/consent", { params: query })
      .then((info) => {
        if (cancelled) return;
        setWorkspaceId(info.workspaces[0]?.id ?? "");
        setPhase({ kind: "ready", info });
      })
      .catch((err) => {
        if (cancelled) return;
        const status = (err as { status?: number } | null)?.status;
        // Session expired mid-way: api() has already sent the browser to a bare
        // /login, which would drop the authorize query. Send it to the same
        // page with the query carried instead.
        if (isSessionExpired(err)) return toLogin();
        // Fixed copy, not the server's text: the server's reason names the
        // app's registration details, which are for its developer, not for you.
        // Only a 4xx means the request itself is bad; anything else is worth
        // another try.
        const invalid = status !== undefined && status >= 400 && status < 500;
        setPhase({
          kind: "error",
          retryable: !invalid,
          message: invalid
            ? t("oauthConsent.errorInvalid")
            : t("oauthConsent.errorLoad"),
        });
      });
    return () => {
      cancelled = true;
    };
    // `query` is rebuilt every render; queryKey is its value.
  }, [isAuthenticated, queryKey, reloadKey]);

  if (authLoading) {
    return (
      <Shell>
        <Skeleton className="h-64 w-full" />
      </Shell>
    );
  }

  if (!isAuthenticated) {
    // Back to this exact URL after login — the whole authorize query rides in
    // the redirect param, so nothing is lost on the way through /login.
    const back = `${location.pathname}${location.search}`;
    return <Navigate to={`/login?redirect=${encodeURIComponent(back)}`} replace />;
  }

  const decide = async (allow: boolean) => {
    setSubmitting(allow ? "allow" : "deny");
    setActionError(null);
    // An identity-only sign-in binds to no workspace, so it sends none.
    const identityOnly = phase.kind === "ready" && phase.info.identity_only === true;
    try {
      const res = await api<{ redirect_uri: string }>("/api/v1/oauth/consent", {
        method: "POST",
        body: identityOnly
          ? { ...query, allow }
          : { ...query, workspace_id: workspaceId || NIL_UUID, allow },
      });
      if (!isSafeRedirect(res.redirect_uri)) {
        setActionError(t("oauthConsent.errorBadRedirect"));
        setSubmitting(null);
        return;
      }
      setPhase({ kind: "redirecting" });
      window.location.assign(res.redirect_uri);
    } catch (err) {
      // Fixed copy keyed on status, not the server's text (see apiErrorMessage):
      // 403 is the one refusal the user can act on: a viewer, or a workspace
      // they are no longer in. So it gets its own sentence.
      const status = (err as { status?: number } | null)?.status;
      if (isSessionExpired(err)) return toLogin();
      setActionError(
        // The 403 copy is about a workspace role; a sign-in has no workspace.
        status === 403 && !identityOnly
          ? t("oauthConsent.errorForbidden")
          : t("oauthConsent.errorSave"),
      );
      setSubmitting(null);
    }
  };

  if (phase.kind === "loading") {
    return (
      <Shell>
        <Skeleton className="h-64 w-full" />
      </Shell>
    );
  }

  if (phase.kind === "error") {
    return (
      <Shell>
        <div className="space-y-2 text-center" role="alert">
          <h1 className="text-xl font-semibold">{t("oauthConsent.cantConnect")}</h1>
          <p className="text-sm text-muted-foreground">{phase.message}</p>
          <p className="text-sm text-muted-foreground">
            {t("oauthConsent.nothingShared")}{" "}
            {phase.retryable ? "" : t("oauthConsent.goBack")}
          </p>
          {phase.retryable && (
            <Button variant="outline" onClick={() => setReloadKey((k) => k + 1)}>
              {t("oauthConsent.tryAgain")}
            </Button>
          )}
        </div>
      </Shell>
    );
  }

  if (phase.kind === "redirecting") {
    return (
      <Shell>
        <p className="text-center text-sm text-muted-foreground">
          {t("oauthConsent.returning")}
        </p>
      </Shell>
    );
  }

  const { info } = phase;
  const hasWorkspaces = info.workspaces.length > 0;
  const clientName = displayClientName(info.client_name);
  const host = redirectHost(info.redirect_uri, info.redirect_host);
  const loopback = info.loopback_warning || isLoopbackRedirect(info.redirect_uri);

  // Where the browser goes next, and the loopback warning: the trust facts both
  // flows show the same way.
  const trustInfo = (
    <>
      <div className="rounded-lg border border-border bg-muted/40 p-4">
        <p className="text-xs uppercase tracking-wide text-muted-foreground">
          {t("oauthConsent.sentTo")}
        </p>
        <p
          className="mt-1 break-all font-mono text-lg font-semibold"
          data-testid="redirect-host"
        >
          {host}
        </p>
      </div>

      {loopback && (
        <div
          role="alert"
          className="flex gap-3 rounded-lg border border-warning/50 bg-warning/10 p-4 text-sm"
        >
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
          <p>
            <strong>{t("oauthConsent.loopbackTitle")}</strong>{" "}
            {t("oauthConsent.loopbackBody")}
          </p>
        </div>
      )}
    </>
  );

  const signedInAs = (
    <p className="text-xs text-muted-foreground">
      {t("oauthConsent.signedInAs", { email: user?.email })}
    </p>
  );

  if (info.identity_only) {
    // OpenID Connect sign-in for a client the operator configured: it learns
    // who you are and nothing else. No workspace, no connector agent, and no
    // "Mesh has not checked who made it" line, which is about DCR clients.
    return (
      <Shell>
        <div className="space-y-6">
          <h1 className="break-words text-2xl font-bold">
            {t("oauthConsent.identityTitle", { name: clientName })}
          </h1>

          {trustInfo}

          <p className="flex gap-1.5 text-sm text-muted-foreground">
            <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0" />
            {t("oauthConsent.identityNote")}
          </p>

          {actionError && (
            <p className="text-sm text-destructive" role="alert">
              {actionError}
            </p>
          )}

          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button
              variant="outline"
              onClick={() => decide(false)}
              disabled={submitting !== null}
            >
              {submitting === "deny"
                ? t("oauthConsent.identityDenying")
                : t("oauthConsent.identityDeny")}
            </Button>
            <Button onClick={() => decide(true)} disabled={submitting !== null}>
              {submitting === "allow"
                ? t("oauthConsent.identityAllowing")
                : t("oauthConsent.identityAllow")}
            </Button>
          </div>

          {signedInAs}
        </div>
      </Shell>
    );
  }

  return (
    <Shell>
      <div className="space-y-6">
        <div className="space-y-1">
          <p className="text-sm text-muted-foreground">
            {t("oauthConsent.intro")}
          </p>
          <h1 className="break-words text-2xl font-bold">{clientName}</h1>
        </div>

        {trustInfo}

        <div className="space-y-1.5">
          <label htmlFor="consent-workspace" className="text-sm font-medium">
            {t("oauthConsent.workspace")}
          </label>
          {hasWorkspaces ? (
            <Select
              id="consent-workspace"
              value={workspaceId}
              onChange={(e) => setWorkspaceId(e.target.value)}
              disabled={submitting !== null}
            >
              {info.workspaces.map((ws) => (
                <option key={ws.id} value={ws.id}>
                  {ws.name}
                </option>
              ))}
            </Select>
          ) : (
            <p className="text-sm text-muted-foreground">
              {t("oauthConsent.noWorkspaces")}
            </p>
          )}
        </div>

        <div className="space-y-2">
          <p className="flex items-center gap-1.5 text-sm font-medium">
            <ShieldCheck className="h-4 w-4 text-muted-foreground" />
            {t("oauthConsent.willBeAbleTo", { name: clientName })}
          </p>
          <ul className="list-disc space-y-1 pl-9 text-sm text-muted-foreground">
            <li>{t("oauthConsent.perm1")}</li>
            <li>{t("oauthConsent.perm2")}</li>
            <li>{t("oauthConsent.perm3")}</li>
            <li>{t("oauthConsent.perm4")}</li>
          </ul>
          <p className="text-sm text-muted-foreground">
            {t("oauthConsent.agentNote")}
          </p>
        </div>

        {actionError && (
          <p className="text-sm text-destructive" role="alert">
            {actionError}
          </p>
        )}

        <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button
            variant="outline"
            onClick={() => decide(false)}
            disabled={submitting !== null}
          >
            {submitting === "deny" ? t("oauthConsent.denying") : t("oauthConsent.deny")}
          </Button>
          <Button
            onClick={() => decide(true)}
            disabled={submitting !== null || !hasWorkspaces || !workspaceId}
          >
            {submitting === "allow" ? t("oauthConsent.allowing") : t("oauthConsent.allow")}
          </Button>
        </div>

        {signedInAs}
      </div>
    </Shell>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <div className="w-full max-w-md rounded-xl border border-border bg-card p-6 text-card-foreground shadow-sm sm:p-8">
        {children}
      </div>
    </div>
  );
}
