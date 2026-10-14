import { useCallback, useEffect, useState } from "react";
import { Bell, BellOff, BellRing } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { api } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import { cn } from "@/lib/cn";

/** Mirrors domain.DocumentWatchState. */
export interface DocumentWatchState {
  watching: boolean;
  source?: string;
  muted: boolean;
  watcher_count: number;
}

/**
 * Follow a document's changes.
 *
 * Two things this button is careful about, both of which come from the fact
 * that subscriptions here can be created for you:
 *
 *   - It says WHY you are subscribed when you did not choose to be ("you
 *     created this page", "you commented"). A toggle that is already on with no
 *     explanation reads as a bug, and the first instinct is to press it off.
 *   - Turning it off is recorded as a refusal, not as an absence, so commenting
 *     again does not silently re-subscribe you. The button therefore shows a
 *     distinct "muted" state rather than falling back to plain "not watching".
 */
export function DocWatchToggle({
  documentId,
  className,
}: {
  documentId: string;
  className?: string;
}) {
  const { t } = useTranslation("docs");
  const [state, setState] = useState<DocumentWatchState | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setState(null);
    api<DocumentWatchState>(`/api/v1/documents/${documentId}/watch`)
      .then((s) => {
        if (!cancelled) setState(s);
      })
      .catch(() => {
        // A subscription state we could not read is not an error worth a toast:
        // the page itself is fine and the button simply stays unavailable.
        if (!cancelled) setState(null);
      });
    return () => {
      cancelled = true;
    };
  }, [documentId]);

  const toggle = useCallback(async () => {
    if (!state || busy) return;
    setBusy(true);
    const subscribing = !state.watching;
    try {
      const next = await api<DocumentWatchState>(
        `/api/v1/documents/${documentId}/watch`,
        { method: subscribing ? "PUT" : "DELETE" },
      );
      setState(next);
      toast.success(
        subscribing
          ? t("watch.started")
          : t("watch.stopped"),
      );
    } catch (error) {
      toast.error(apiErrorMessage(error, t("watch.failed")));
    } finally {
      setBusy(false);
    }
  }, [busy, documentId, state, t]);

  if (!state) return null;

  const Icon = state.watching ? BellRing : state.muted ? BellOff : Bell;

  // The explanation is in the tooltip rather than the label: the label has to
  // stay one word wide in a row that already holds four controls.
  const why = state.watching
    ? state.source === "author"
      ? t("watch.whyAuthor")
      : state.source === "commenter"
        ? t("watch.whyCommenter")
        : t("watch.whyWatching")
    : state.muted
      ? t("watch.whyMuted")
      : t("watch.whyNotWatching");

  return (
    <Button
      type="button"
      variant={state.watching ? "secondary" : "ghost"}
      size="sm"
      data-testid="doc-watch-toggle"
      aria-pressed={state.watching}
      title={why}
      aria-label={why}
      disabled={busy}
      className={cn("h-7 gap-1.5 px-2 text-xs", className)}
      onClick={() => void toggle()}
    >
      <Icon className="h-3.5 w-3.5" />
      {state.watching ? t("watch.watching") : t("watch.watch")}
      {state.watcher_count > 1 && (
        <span
          className="rounded bg-muted px-1 text-[11px] font-medium tabular-nums text-muted-foreground"
          title={t("watch.count", { count: state.watcher_count })}
        >
          {state.watcher_count}
        </span>
      )}
    </Button>
  );
}
