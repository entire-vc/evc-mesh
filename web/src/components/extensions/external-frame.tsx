import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import { api } from "@/lib/api";
import { currentLanguage } from "@/i18n";
import { useWorkspaceStore } from "@/stores/workspace";
import { toast } from "@/components/ui/toast";
import {
  contextMessage,
  frameSrc,
  parseFrameMessage,
  type FrameCommand,
} from "@/lib/extension-frame-protocol";

/** A screen that has not loaded in this long shows "unavailable"; the host never waits on it. */
export const FRAME_LOAD_TIMEOUT_MS = 10_000;

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
  slot: "sidebar.item" | "task.panel";
  taskId?: string;
  projectId?: string;
  /** `screen`: fills the whole area (sidebar item). `panel`: fills the tab and follows `mesh.resize` as a minimum height. */
  layout: "screen" | "panel";
}

type State =
  | { kind: "loading" }
  | { kind: "ready"; ctx: UiContext }
  | { kind: "error" };

/**
 * Iframe screen of an external extension (W3.3). Mesh asks the server for a
 * short-lived context token (the extension's own token never reaches the
 * browser), puts it on the screen's address as `?token=` and again in a
 * `mesh.context` message, and obeys only what the frame itself says
 * (see `lib/extension-frame-protocol.ts`).
 */
export function ExternalFrame({ extensionId, name, slot, taskId, projectId, layout }: ExternalFrameProps) {
  const { t } = useTranslation("task");
  const navigate = useNavigate();
  const ws = useWorkspaceStore((s) => s.currentWorkspace);
  const [state, setState] = useState<State>({ kind: "loading" });
  const [late, setLate] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [minHeight, setMinHeight] = useState<number | null>(null);
  const [attempt, setAttempt] = useState(0);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const ctxRef = useRef<UiContext | null>(null);

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
    setState({ kind: "loading" });
    setLoaded(false);
    setLate(false);
    fetchContext()
      .then((ctx) => {
        if (cancelled) return;
        ctxRef.current = ctx;
        setState({ kind: "ready", ctx });
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
        }),
        ctx.origin,
      );
    },
    [wsId, wsSlug, projectId, taskId],
  );

  const onCommand = useCallback(
    (cmd: FrameCommand) => {
      switch (cmd.type) {
        case "ready":
          // The token on the address lives 60 s: a screen that asks gets a fresh one.
          fetchContext()
            .then((ctx) => {
              ctxRef.current = ctx;
              send(ctx);
            })
            .catch(() => undefined);
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
      }
    },
    [fetchContext, send, layout, navigate],
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

  if (state.kind === "error" || (late && !loaded)) {
    return (
      <div className="flex flex-1 flex-col items-start gap-2 p-4 text-sm text-muted-foreground" role="status" data-testid="external-frame-unavailable">
        <span>{t("slots.unavailable", { name })}</span>
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
          src={frameSrc(state.ctx.frame_url, state.ctx.token)}
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
