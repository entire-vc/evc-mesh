import { useEffect, useState } from "react";
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

type AuthorizeQuery = Record<(typeof AUTHORIZE_PARAMS)[number], string>;

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
  return name.replace(INVISIBLE_CHARS, "").trim() || "Unnamed app";
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
  const { isAuthenticated, isLoading: authLoading, user } = useAuthStore();
  const location = useLocation();
  const [searchParams] = useSearchParams();

  const query = {} as AuthorizeQuery;
  for (const key of AUTHORIZE_PARAMS) query[key] = searchParams.get(key) ?? "";
  const queryKey = AUTHORIZE_PARAMS.map((k) => query[k]).join("\n");

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
            ? "This connection request is not valid."
            : "Could not load this request. Check your connection and try again.",
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
    try {
      const res = await api<{ redirect_uri: string }>("/api/v1/oauth/consent", {
        method: "POST",
        body: {
          ...query,
          workspace_id: workspaceId || NIL_UUID,
          allow,
        },
      });
      if (!isSafeRedirect(res.redirect_uri)) {
        setActionError("The app sent an address this page will not open.");
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
        status === 403
          ? "Your role in this workspace does not allow connecting apps. Pick another workspace, or ask an admin or owner."
          : "Could not save your choice. Try again.",
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
          <h1 className="text-xl font-semibold">Can't connect this app</h1>
          <p className="text-sm text-muted-foreground">{phase.message}</p>
          <p className="text-sm text-muted-foreground">
            Nothing was shared.{" "}
            {phase.retryable
              ? ""
              : "Go back to the app and start the connection again."}
          </p>
          {phase.retryable && (
            <Button variant="outline" onClick={() => setReloadKey((k) => k + 1)}>
              Try again
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
          Returning you to the app…
        </p>
      </Shell>
    );
  }

  const { info } = phase;
  const hasWorkspaces = info.workspaces.length > 0;
  const clientName = displayClientName(info.client_name);
  const host = redirectHost(info.redirect_uri, info.redirect_host);
  const loopback = info.loopback_warning || isLoopbackRedirect(info.redirect_uri);

  return (
    <Shell>
      <div className="space-y-6">
        <div className="space-y-1">
          <p className="text-sm text-muted-foreground">
            An app is asking to connect to Mesh. Mesh has not checked who made it.
          </p>
          <h1 className="break-words text-2xl font-bold">{clientName}</h1>
        </div>

        <div className="rounded-lg border border-border bg-muted/40 p-4">
          <p className="text-xs uppercase tracking-wide text-muted-foreground">
            After you decide, you'll be sent to
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
              <strong>This access goes to a program on this computer.</strong> The app
              gave an address that points at your own machine, not at a website. Continue
              only if you just started this connection yourself.
            </p>
          </div>
        )}

        <div className="space-y-1.5">
          <label htmlFor="consent-workspace" className="text-sm font-medium">
            Workspace
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
              You are not a member of any workspace, so there is nothing to connect.
            </p>
          )}
        </div>

        <div className="space-y-2">
          <p className="flex items-center gap-1.5 text-sm font-medium">
            <ShieldCheck className="h-4 w-4 text-muted-foreground" />
            {clientName} will be able to, in the selected workspace:
          </p>
          <ul className="list-disc space-y-1 pl-9 text-sm text-muted-foreground">
            <li>Read its projects, tasks, comments, documents and memory</li>
            <li>Create, change and delete tasks; write comments and memory</li>
            <li>Attach files and post events</li>
            <li>Change workflow rules, only if you are an admin or owner</li>
          </ul>
          <p className="text-sm text-muted-foreground">
            It acts as a connector agent, supervised by you. It cannot manage members.
            Its access follows your role in the workspace and stops if you lose that
            role. You can revoke it at any time under Connected apps.
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
            {submitting === "deny" ? "Denying…" : "Deny"}
          </Button>
          <Button
            onClick={() => decide(true)}
            disabled={submitting !== null || !hasWorkspaces || !workspaceId}
          >
            {submitting === "allow" ? "Allowing…" : "Allow"}
          </Button>
        </div>

        <p className="text-xs text-muted-foreground">
          Signed in as {user?.email}
        </p>
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
