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
  REVOKED_DELIVERY_GRACE_MS,
  revokedMessage,
  themeMessage,
  widgetRefreshMessage,
  widgetSettingsMessage,
  widgetVisibilityMessage,
  type FrameCommand,
  type OpenTarget,
} from "@/lib/extension-frame-protocol";
import { acquireWidgetLoadSlot } from "@/lib/widget-load-queue";
import { themeKey, useWidgetTheme } from "@/lib/widget-theme";

/** No `mesh.app-ready` in this long (counted from the start of the load) is an error with "Retry". */
export const WIDGET_APP_READY_TIMEOUT_MS = 15_000;
/** After a transient refresh failure (401, 5xx, network) the next try comes this much later. */
export const WIDGET_REFRESH_RETRY_MS = 15_000;
/** A widget starts to load this close before it scrolls into view. */
const VISIBILITY_MARGIN = "100px";
/** The longest delay setTimeout keeps (2^31-1 ms); a longer one fires immediately. */
const MAX_TIMER_MS = 2_147_483_647;

interface UiContext {
  token: string;
  expires_at: string;
  frame_url: string;
  origin: string;
  title: string;
}

export interface ExternalWidgetFrameProps {
  extensionId: string;
  name: string;
  /** The widget id of the manifest's `widgets[]` (the part after the dot of the instance type);
   *  Mesh resolves the screen from its `render.frame`, and the screen reads it from `mesh.context.widget.type`. */
  widgetType: string;
  dashboardId: string;
  instanceId: string;
  /** Set for a project-scoped widget (`scope: project`): the token names the project. */
  projectId?: string;
  settings?: Record<string, unknown>;
  size?: { w: number; h: number };
  /** Bump to tell the widget to reload its data (`mesh.widget.refresh`). */
  refreshNonce?: number;
  /** The widget asked for its settings (`mesh.widget.configure`). */
  onConfigure?: () => void;
}

type State =
  | { kind: "idle" }
  | { kind: "queued" }
  | { kind: "loading" }
  | { kind: "ready"; ctx: UiContext }
  | { kind: "error"; why: "unavailable" | "timeout" }
  /** `draining`: the revoked frame, kept hidden until `mesh.revoked` reaches it (REVOKED_DELIVERY_GRACE_MS). */
  | { kind: "revoked"; draining: UiContext | null }
  | { kind: "app-error"; message: string };

const EMPTY_SETTINGS: Record<string, unknown> = {};
const DEFAULT_SIZE = { w: 2, h: 2 };

/** Path of the Mesh page for a target the widget asked to open, after Mesh confirmed the person may see it. */
async function openPathFor(target: OpenTarget, wsSlug: string): Promise<string> {
  const id = encodeURIComponent(target.id);
  if (target.kind === "task") {
    await api(`/api/v1/tasks/${id}`);
    return `/t/${id}`;
  }
  if (target.kind === "doc") {
    await api(`/api/v1/documents/${id}`);
    return `/d/${id}`;
  }
  const project = await api<{ slug: string }>(`/api/v1/projects/${id}`);
  return `/w/${encodeURIComponent(wsSlug)}/p/${encodeURIComponent(project.slug)}`;
}

/**
 * An external extension's widget on a dashboard (`dashboard.widget`, protocol v2,
 * docs/extensions/ui-iframe.md). Differences from `ExternalFrame`:
 *  - the iframe is created only once the widget is near the viewport, and at most
 *    six frames load at a time (`widget-load-queue.ts`);
 *  - a widget that does not send `mesh.app-ready` within 15 s shows an error and "Retry";
 *  - Mesh tells it the theme, its settings, its visibility and when to refresh;
 *  - `mesh.resize` is ignored (the dashboard cell sets the size);
 *  - `mesh.open` opens a task, document or project only after Mesh itself checked access.
 */
export function ExternalWidgetFrame({
  extensionId,
  name,
  widgetType,
  dashboardId,
  instanceId,
  projectId,
  settings = EMPTY_SETTINGS,
  size = DEFAULT_SIZE,
  refreshNonce = 0,
  onConfigure,
}: ExternalWidgetFrameProps) {
  const { t } = useTranslation("task");
  const navigate = useNavigate();
  const ws = useWorkspaceStore((s) => s.currentWorkspace);
  const wsId = ws?.id;
  const wsSlug = ws?.slug ?? "";
  const theme = useWidgetTheme();

  const [state, setState] = useState<State>({ kind: "idle" });
  const [attempt, setAttempt] = useState(0);
  const [visible, setVisible] = useState(false);
  const [wanted, setWanted] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [appReady, setAppReady] = useState(false);
  const [refreshAt, setRefreshAt] = useState<number | null>(null);

  const hostRef = useRef<HTMLDivElement>(null);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const ctxRef = useRef<UiContext | null>(null);
  const genRef = useRef(0);
  const releaseRef = useRef<(() => void) | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  // Latest values for the callbacks below; messages are built from these at send time.
  const themeSig = themeKey(theme);
  const settingsSig = JSON.stringify(settings) + `${size.w}x${size.h}`;
  const live = useRef({ settings, size, theme, visible, projectId, themeSig, settingsSig });
  live.current = { settings, size, theme, visible, projectId, themeSig, settingsSig };
  // What the frame has been told so far: a mesh.context carries all of it, later changes go as their own message.
  const sent = useRef({ theme: themeSig, settings: settingsSig, visible, nonce: refreshNonce });

  const giveBackSlot = useCallback(() => {
    releaseRef.current?.();
    releaseRef.current = null;
  }, []);

  // 1. Visibility: the frame is wanted from the first time the widget comes near the viewport.
  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    if (typeof IntersectionObserver === "undefined") {
      setVisible(true);
      setWanted(true);
      return;
    }
    const io = new IntersectionObserver(
      (entries) => {
        const last = entries[entries.length - 1];
        if (!last) return;
        setVisible(last.isIntersecting);
        if (last.isIntersecting) setWanted(true);
      },
      { rootMargin: VISIBILITY_MARGIN },
    );
    io.observe(el);
    return () => io.disconnect();
  }, []);

  const fetchContext = useCallback(
    () =>
      api<UiContext>(`/api/v1/ext/${extensionId}/ui-context`, {
        method: "POST",
        body: {
          workspace_id: wsId,
          slot: "dashboard.widget",
          dashboard_id: dashboardId,
          instance_id: instanceId,
          widget_id: widgetType,
          ...(projectId ? { project_id: projectId } : {}),
        },
      }),
    [extensionId, wsId, dashboardId, instanceId, widgetType, projectId],
  );

  // 2. Once wanted: wait for one of the six load slots, fetch the context, and give the slot back
  //    when the widget is up, failed, timed out or removed.
  useEffect(() => {
    if (!wsId || !wanted) return;
    let cancelled = false;
    genRef.current += 1;
    ctxRef.current = null;
    setLoaded(false);
    setAppReady(false);
    setRefreshAt(null);
    setState({ kind: "queued" });
    let timer: ReturnType<typeof setTimeout> | undefined;
    // Set once the 15 s deadline passed: a context that arrives later must not bring the frame back
    // (its queue slot is already given back); only Retry starts a new load.
    let timedOut = false;
    giveBackSlot();
    releaseRef.current = acquireWidgetLoadSlot(() => {
      if (cancelled) return;
      setState({ kind: "loading" });
      timer = setTimeout(() => {
        if (cancelled) return;
        timedOut = true;
        genRef.current += 1;
        ctxRef.current = null;
        giveBackSlot();
        setRefreshAt(null);
        setState({ kind: "error", why: "timeout" });
      }, WIDGET_APP_READY_TIMEOUT_MS);
      timerRef.current = timer;
      fetchContext()
        .then((ctx) => {
          if (cancelled || timedOut) return;
          ctxRef.current = ctx;
          setState({ kind: "ready", ctx });
          setRefreshAt(Date.now() + refreshDelayMs(ctx.expires_at, Date.now()));
        })
        .catch(() => {
          if (cancelled || timedOut) return;
          clearTimeout(timer);
          giveBackSlot();
          setState({ kind: "error", why: "unavailable" });
        });
    });
    return () => {
      cancelled = true;
      clearTimeout(timer);
      giveBackSlot();
    };
  }, [wsId, wanted, fetchContext, attempt, giveBackSlot]);

  const settle = useCallback(() => {
    clearTimeout(timerRef.current);
    giveBackSlot();
  }, [giveBackSlot]);

  const tell = useCallback((msg: object) => {
    const win = iframeRef.current?.contentWindow;
    const origin = ctxRef.current?.origin;
    if (win && origin) win.postMessage(msg, origin);
  }, []);

  const sendContext = useCallback(
    (ctx: UiContext) => {
      const win = iframeRef.current?.contentWindow;
      if (!win || !wsId) return;
      const cur = live.current;
      sent.current.theme = cur.themeSig;
      sent.current.settings = cur.settingsSig;
      sent.current.visible = cur.visible;
      win.postMessage(
        contextMessage({
          token: ctx.token,
          expiresAt: ctx.expires_at,
          workspaceId: wsId,
          workspaceSlug: wsSlug,
          projectId: cur.projectId,
          locale: currentLanguage(),
          widget: { type: widgetType, instanceId, dashboardId, settings: cur.settings, size: cur.size, theme: cur.theme },
        }),
        ctx.origin,
      );
      win.postMessage(widgetVisibilityMessage(cur.visible), ctx.origin);
    },
    [wsId, wsSlug, widgetType, instanceId, dashboardId],
  );

  const refresh = useCallback(() => {
    const gen = genRef.current;
    fetchContext()
      .then((ctx) => {
        if (gen !== genRef.current) return;
        ctxRef.current = ctx;
        sendContext(ctx);
        setRefreshAt(Date.now() + refreshDelayMs(ctx.expires_at, Date.now()));
      })
      .catch((err: unknown) => {
        if (gen !== genRef.current) return;
        const status = err instanceof ApiRequestError ? err.status : 0;
        tell(contextErrorMessage(status));
        if (isRevocation(status)) {
          // mesh.revoked is the last word: the context goes first, so nothing more (theme, settings, a token) reaches
          // the kept frame and nothing it sends is handled any longer; the message itself goes to the saved origin.
          const last = ctxRef.current;
          ctxRef.current = null;
          genRef.current += 1;
          if (last) iframeRef.current?.contentWindow?.postMessage(revokedMessage(status), last.origin);
          setRefreshAt(null);
          settle();
          // The frame keeps the context it was opened with: its address must not change, or it reloads.
          setState((prev) => ({ kind: "revoked", draining: prev.kind === "ready" ? prev.ctx : null }));
        } else {
          setRefreshAt(Date.now() + WIDGET_REFRESH_RETRY_MS);
        }
      });
  }, [fetchContext, sendContext, tell, settle]);

  // The revoked frame leaves the page only after the message had time to reach it.
  const draining = state.kind === "revoked" ? state.draining : null;
  useEffect(() => {
    if (!draining) return;
    const id = setTimeout(() => setState({ kind: "revoked", draining: null }), REVOKED_DELIVERY_GRACE_MS);
    return () => clearTimeout(id);
  }, [draining]);

  // A browser timer holds at most ~24.8 days: a farther expiry is reached in steps, never fired at once.
  const [refreshStep, setRefreshStep] = useState(0);
  useEffect(() => {
    if (state.kind !== "ready" || !loaded || refreshAt === null) return;
    const delay = Math.max(0, refreshAt - Date.now());
    const id = delay > MAX_TIMER_MS ? setTimeout(() => setRefreshStep((n) => n + 1), MAX_TIMER_MS) : setTimeout(refresh, delay);
    return () => clearTimeout(id);
  }, [state.kind, loaded, refreshAt, refresh, refreshStep]);

  // 3. Changes after the first context travel by message, never by reload.
  useEffect(() => {
    if (!appReady && !loaded) return;
    const s = sent.current;
    if (s.theme !== themeSig) {
      s.theme = themeSig;
      tell(themeMessage(live.current.theme));
    }
    if (s.settings !== settingsSig) {
      s.settings = settingsSig;
      tell(widgetSettingsMessage(live.current.settings, live.current.size));
    }
    if (s.visible !== visible) {
      s.visible = visible;
      tell(widgetVisibilityMessage(visible));
    }
    if (s.nonce !== refreshNonce) {
      s.nonce = refreshNonce;
      tell(widgetRefreshMessage());
    }
  }, [themeSig, settingsSig, visible, refreshNonce, appReady, loaded, tell]);

  const open = useCallback(
    (target: OpenTarget) => {
      openPathFor(target, wsSlug)
        .then((path) => navigate(path))
        .catch(() => toast.error(t("slots.openDenied")));
    },
    [wsSlug, navigate, t],
  );

  const onCommand = useCallback(
    (cmd: FrameCommand) => {
      switch (cmd.type) {
        case "ready":
          refresh();
          break;
        case "app-ready":
          setAppReady(true);
          settle();
          break;
        case "app-error":
          setRefreshAt(null);
          settle();
          setState({ kind: "app-error", message: cmd.message });
          break;
        case "open":
          open(cmd.target);
          break;
        case "configure":
          onConfigure?.();
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
        // `mesh.resize` and `mesh.state` mean nothing here: the dashboard cell sets the size and a widget has no address.
        default:
          break;
      }
    },
    [refresh, settle, open, onConfigure, navigate],
  );

  const origin = state.kind === "ready" ? state.ctx.origin : null;
  useEffect(() => {
    if (!origin) return;
    const handler = (ev: MessageEvent) => {
      // Checked on every message, not only by removing the listener: after mesh.revoked the context is gone at once,
      // while the listener leaves only after the next commit.
      if (!ctxRef.current) return;
      const cmd = parseFrameMessage(ev, { origin, frameWindow: iframeRef.current?.contentWindow });
      if (cmd) onCommand(cmd);
    };
    window.addEventListener("message", handler);
    return () => window.removeEventListener("message", handler);
  }, [origin, onCommand]);

  const failed = state.kind === "error" || state.kind === "revoked" || state.kind === "app-error";
  // The iframe keeps its place in the tree through a revocation: a remount would reload it and lose the message.
  const frameCtx = state.kind === "ready" ? state.ctx : draining;
  const retry = () => setAttempt((n) => n + 1);

  return (
    <div ref={hostRef} className="relative flex min-h-[8rem] flex-1 flex-col" data-testid="external-widget" data-widget-state={state.kind} data-app-ready={appReady ? "true" : undefined}>
      {failed ? (
        <div
          className="flex flex-1 flex-col items-start gap-2 p-4 text-sm text-muted-foreground"
          role="status"
          data-testid={state.kind === "revoked" ? "external-widget-revoked" : state.kind === "app-error" ? "external-widget-app-error" : "external-widget-error"}
        >
          <span>
            {state.kind === "revoked"
              ? t("slots.revoked", { name })
              : state.kind === "app-error"
                ? t("slots.appError", { name })
                : state.why === "timeout"
                  ? t("slots.widgetTimeout", { name })
                  : t("slots.unavailable", { name })}
          </span>
          {state.kind === "app-error" && state.message ? (
            <span className="max-w-prose break-words text-foreground">{state.message}</span>
          ) : null}
          {state.kind !== "revoked" ? (
            <button type="button" className="rounded border border-border px-2 py-1 text-xs text-foreground hover:bg-muted" onClick={retry}>
              {t("slots.retry")}
            </button>
          ) : null}
        </div>
      ) : null}
      {!failed && !appReady ? (
        <div className="absolute inset-0 space-y-2 p-3" aria-busy="true" data-testid="external-widget-pending">
          <div className="h-4 w-32 animate-pulse rounded bg-muted" />
          <div className="h-10 w-full animate-pulse rounded bg-muted" />
        </div>
      ) : null}
      {frameCtx ? (
        <iframe
          ref={iframeRef}
          title={frameCtx.title || name}
          src={frameSrc(frameCtx.frame_url, frameCtx.token)}
          sandbox="allow-scripts allow-forms allow-same-origin allow-popups allow-popups-to-escape-sandbox"
          referrerPolicy="no-referrer"
          allow="clipboard-write"
          className={state.kind === "ready" ? "h-full w-full flex-1 border-0 bg-transparent" : "hidden"}
          aria-hidden={state.kind === "ready" ? undefined : true}
          tabIndex={state.kind === "ready" ? undefined : -1}
          style={{ colorScheme: theme.mode }}
          onLoad={() => {
            setLoaded(true);
            if (ctxRef.current) sendContext(ctxRef.current);
          }}
        />
      ) : null}
    </div>
  );
}
