import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import { api, ApiRequestError } from "@/lib/api";
import { currentLanguage } from "@/i18n";
import { useWorkspaceStore } from "@/stores/workspace";
import { toast } from "@/components/ui/toast";
import {
  contextErrorMessage,
  contextMessage,
  frameSrc,
  isRevocation,
  parseFrameMessage,
  refreshDelayMs,
  revokedMessage,
  type FrameCommand,
} from "@/lib/extension-frame-protocol";

/** A screen that has not loaded in this long shows "unavailable"; the host never waits on it. */
export const FRAME_LOAD_TIMEOUT_MS = 10_000;
/** After a transient refresh failure (401, 5xx, network) the next try comes this much later. */
export const REFRESH_RETRY_MS = 15_000;

interface UiContext {
  token: string;
  expires_at: string;
  frame_url: string;
  origin: string;
  title: string;
}

export interface ExternalFrameProps {
  extensionId: string;
  name: string;
  slot: "sidebar.item" | "task.panel" | "project.view";
  taskId?: string;
  projectId?: string;
  /**
   * The section of the screen the Mesh address points at (`/memory/search#q`,
   * see `normalizeSection`). Opens the frame there and is resent in
   * `mesh.context` when it changes from outside (back/forward).
   */
  section?: string;
  /** The frame reported a new section (`mesh.state`); the page puts it into its address. */
  onSection?: (section: string, replace: boolean) => void;
  /** `screen`: fills the whole area (sidebar item). `panel`: fills the tab and follows `mesh.resize` as a minimum height. */
  layout: "screen" | "panel";
}

type State =
  | { kind: "loading" }
  | { kind: "ready"; ctx: UiContext; section: string }
  | { kind: "error" }
  | { kind: "revoked" }
  | { kind: "app-error"; message: string };

/**
 * Iframe screen of an external extension (W3.3). Mesh asks the server for a
 * short-lived context token (the extension's own token never reaches the
 * browser), puts it on the screen's address as `?token=` and again in a
 * `mesh.context` message, and obeys only what the frame itself says
 * (see `lib/extension-frame-protocol.ts`). The token is renewed on its own at
 * 80% of its lifetime; a refused renewal is told to the frame
 * (`mesh.context.error`), and one that means access is gone also revokes it
 * (`mesh.revoked`) and closes the screen.
 */
export function ExternalFrame({ extensionId, name, slot, taskId, projectId, layout, section = "", onSection }: ExternalFrameProps) {
  const { t } = useTranslation("task");
  const navigate = useNavigate();
  const ws = useWorkspaceStore((s) => s.currentWorkspace);
  const [state, setState] = useState<State>({ kind: "loading" });
  const [late, setLate] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [minHeight, setMinHeight] = useState<number | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [appReady, setAppReady] = useState(false);
  /** Absolute time of the next proactive token renewal. */
  const [refreshAt, setRefreshAt] = useState<number | null>(null);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const ctxRef = useRef<UiContext | null>(null);
  // The section the frame is at as far as Mesh knows: set from the address, or from the frame's own mesh.state.
  const sectionRef = useRef(section);
  /** Bumped on every (re)start, so an answer for a screen that was retried meanwhile is dropped. */
  const genRef = useRef(0);

  const wsId = ws?.id;
  const wsSlug = ws?.slug ?? "";

  const fetchContext = useCallback(
    () =>
      api<UiContext>(`/api/v1/ext/${extensionId}/ui-context`, {
        method: "POST",
        body: { workspace_id: wsId, slot, ...(taskId ? { task_id: taskId } : {}), ...(projectId && !taskId ? { project_id: projectId } : {}) },
      }),
    [extensionId, wsId, slot, taskId, projectId],
  );

  useEffect(() => {
    if (!wsId) return;
    let cancelled = false;
    genRef.current += 1;
    ctxRef.current = null;
    setState({ kind: "loading" });
    setLoaded(false);
    setLate(false);
    setAppReady(false);
    setRefreshAt(null);
    fetchContext()
      .then((ctx) => {
        if (cancelled) return;
        ctxRef.current = ctx;
        // The src is built once per load: later section changes travel by message, never by reload.
        setState({ kind: "ready", ctx, section: sectionRef.current });
        setRefreshAt(Date.now() + refreshDelayMs(ctx.expires_at, Date.now()));
      })
      .catch(() => {
        if (!cancelled) setState({ kind: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [wsId, fetchContext, attempt]);

  useEffect(() => {
    if (state.kind !== "ready" || loaded) return;
    const id = setTimeout(() => setLate(true), FRAME_LOAD_TIMEOUT_MS);
    return () => clearTimeout(id);
  }, [state, loaded]);

  const send = useCallback(
    (ctx: UiContext) => {
      const win = iframeRef.current?.contentWindow;
      if (!win || !wsId) return;
      win.postMessage(
        contextMessage({
          token: ctx.token,
          expiresAt: ctx.expires_at,
          workspaceId: wsId,
          workspaceSlug: wsSlug,
          projectId,
          taskId,
          locale: currentLanguage(),
          section: sectionRef.current,
        }),
        ctx.origin,
      );
    },
    [wsId, wsSlug, projectId, taskId],
  );

  // The address moved to another section without the frame asking (back/forward, a link): tell the frame.
  useEffect(() => {
    if (section === sectionRef.current) return;
    sectionRef.current = section;
    if (loaded && ctxRef.current) send(ctxRef.current);
  }, [section, loaded, send]);

  /** Post a lifecycle message to the frame, on the service origin only. */
  const tell = useCallback((msg: object) => {
    const win = iframeRef.current?.contentWindow;
    const origin = ctxRef.current?.origin;
    if (win && origin) win.postMessage(msg, origin);
  }, []);

  /** Renew the token and hand it over; a refusal is told to the frame and may revoke it. */
  const refresh = useCallback(() => {
    const gen = genRef.current;
    fetchContext()
      .then((ctx) => {
        if (gen !== genRef.current) return;
        ctxRef.current = ctx;
        send(ctx);
        setRefreshAt(Date.now() + refreshDelayMs(ctx.expires_at, Date.now()));
      })
      .catch((err: unknown) => {
        if (gen !== genRef.current) return;
        const status = err instanceof ApiRequestError ? err.status : 0;
        tell(contextErrorMessage(status));
        if (isRevocation(status)) {
          tell(revokedMessage(status));
          setRefreshAt(null);
          setState({ kind: "revoked" });
        } else {
          setRefreshAt(Date.now() + REFRESH_RETRY_MS);
        }
      });
  }, [fetchContext, send, tell]);

  useEffect(() => {
    if (state.kind !== "ready" || !loaded || refreshAt === null) return;
    const id = setTimeout(refresh, Math.max(0, refreshAt - Date.now()));
    return () => clearTimeout(id);
  }, [state.kind, loaded, refreshAt, refresh]);

  const onCommand = useCallback(
    (cmd: FrameCommand) => {
      switch (cmd.type) {
        case "ready":
          // The token on the address lives 60 s: a screen that asks gets a fresh one.
          refresh();
          break;
        case "app-ready":
          setAppReady(true);
          break;
        case "app-error":
          setRefreshAt(null);
          setState({ kind: "app-error", message: cmd.message });
          break;
        case "resize":
          if (layout === "panel") setMinHeight(cmd.height);
          break;
        case "navigate":
          navigate(cmd.path);
          break;
        case "toast":
          if (cmd.level === "error") toast.error(cmd.message);
          else if (cmd.level === "success") toast.success(cmd.message);
          else if (cmd.level === "warning") toast.warning(cmd.message);
          else toast.message(cmd.message);
          break;
        case "state":
          if (cmd.section === sectionRef.current) break;
          sectionRef.current = cmd.section;
          onSection?.(cmd.section, cmd.replace);
          break;
      }
    },
    [refresh, layout, navigate, onSection],
  );

  const origin = state.kind === "ready" ? state.ctx.origin : null;
  useEffect(() => {
    if (!origin) return;
    const handler = (ev: MessageEvent) => {
      const cmd = parseFrameMessage(ev, { origin, frameWindow: iframeRef.current?.contentWindow });
      if (cmd) onCommand(cmd);
    };
    window.addEventListener("message", handler);
    return () => window.removeEventListener("message", handler);
  }, [origin, onCommand]);

  if (state.kind === "error" || state.kind === "revoked" || state.kind === "app-error" || (late && !loaded)) {
    const testId =
      state.kind === "revoked" ? "external-frame-revoked" : state.kind === "app-error" ? "external-frame-app-error" : "external-frame-unavailable";
    return (
      <div className="flex flex-1 flex-col items-start gap-2 p-4 text-sm text-muted-foreground" role="status" data-testid={testId}>
        <span>
          {state.kind === "revoked"
            ? t("slots.revoked", { name })
            : state.kind === "app-error"
              ? t("slots.appError", { name })
              : t("slots.unavailable", { name })}
        </span>
        {state.kind === "app-error" && state.message ? (
          <span className="max-w-prose break-words text-foreground" data-testid="external-frame-app-error-message">
            {state.message}
          </span>
        ) : null}
        <button
          type="button"
          className="rounded border border-border px-2 py-1 text-xs text-foreground hover:bg-muted"
          onClick={() => setAttempt((n) => n + 1)}
        >
          {t("slots.retry")}
        </button>
      </div>
    );
  }

  return (
    <div
      className={layout === "screen" ? "relative min-h-[60vh] flex-1" : "relative flex min-h-0 flex-1 flex-col"}
      style={layout === "panel" && minHeight ? { minHeight } : undefined}
      data-testid="external-frame"
      data-app-ready={appReady ? "true" : undefined}
    >
      {state.kind === "loading" || !loaded ? (
        <div className="absolute inset-0 space-y-2 p-3" aria-busy="true" data-testid="external-frame-pending">
          <div className="h-4 w-32 animate-pulse rounded bg-muted" />
          <div className="h-10 w-full animate-pulse rounded bg-muted" />
        </div>
      ) : null}
      {state.kind === "ready" && (
        <iframe
          ref={iframeRef}
          title={state.ctx.title || name}
          src={frameSrc(state.ctx.frame_url, state.ctx.token, state.section)}
          sandbox="allow-scripts allow-forms allow-same-origin"
          referrerPolicy="no-referrer"
          allow=""
          className={loaded ? "h-full w-full flex-1 border-0 bg-background" : "h-0 w-0 border-0"}
          onLoad={() => {
            setLoaded(true);
            if (ctxRef.current) send(ctxRef.current);
          }}
        />
      )}
    </div>
  );
}
