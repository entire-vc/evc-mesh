import { Component, Suspense, createContext, useCallback, useContext, useEffect, useMemo, useState, type ErrorInfo, type ReactNode } from "react";
import { MoreHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { Task, VCSProvider } from "@/types";
import { useSlotExtensions } from "@/lib/extension-slots";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { BUILTIN_SLOTS, type SlotAction } from "./builtin-slots";

/** A slot that has not rendered in this long shows "unavailable"; the card never waits for it. */
export const SLOT_TIMEOUT_MS = 2000;

export interface PanelTab {
  extensionId: string;
  name: string;
}

/** `task.panel` tabs to append after the system tabs, ordered by extension name. */
export function usePanelTabs(): PanelTab[] {
  const exts = useSlotExtensions("task.panel");
  return useMemo(
    () => exts.filter((e) => BUILTIN_SLOTS[e.id]?.["task.panel"]).map((e) => ({ extensionId: e.id, name: e.name })),
    [exts],
  );
}

/**
 * VCS providers whose links an active `slot` contribution draws. Only these are hidden
 * from the generic list / card icon: when the extension is off or has no contribution
 * there, the links stay visible in the generic view.
 */
export function useSlotOwnedVcsProviders(slot: "task.panel" | "task.badge"): VCSProvider[] {
  const exts = useSlotExtensions(slot);
  return useMemo(
    () =>
      exts
        .filter((e) => BUILTIN_SLOTS[e.id]?.[slot])
        .map((e) => BUILTIN_SLOTS[e.id]?.ownsVcsProvider)
        .filter((p): p is VCSProvider => Boolean(p)),
    [exts, slot],
  );
}

export interface ResolvedAction extends SlotAction {
  extensionId: string;
  extensionName: string;
}

/** `task.actions` items for the "…" menu. */
export function useSlotActions(): ResolvedAction[] {
  const exts = useSlotExtensions("task.actions");
  return useMemo(
    () =>
      exts.flatMap((e) =>
        (BUILTIN_SLOTS[e.id]?.["task.actions"] ?? []).map((a) => ({ ...a, extensionId: e.id, extensionName: e.name })),
      ),
    [exts],
  );
}

/** `task.badge` contributions for one card; renders nothing when none apply. */
export function SlotBadges({ task }: { task: Task }) {
  const exts = useSlotExtensions("task.badge");
  if (exts.length === 0) return null;
  return (
    <>
      {exts.map((e) => {
        const Badge = BUILTIN_SLOTS[e.id]?.["task.badge"];
        return Badge ? <Badge key={e.id} task={task} /> : null;
      })}
    </>
  );
}

function Unavailable({ name }: { name: string }) {
  const { t } = useTranslation("task");
  return (
    <div className="flex-1 p-4 text-sm text-muted-foreground" role="status" data-testid="slot-unavailable">
      {t("slots.unavailable", { name })}
    </div>
  );
}

function Pending() {
  return (
    <div className="flex-1 space-y-2 p-3" aria-busy="true" data-testid="slot-pending">
      <div className="h-4 w-32 animate-pulse rounded bg-muted" />
      <div className="h-10 w-full animate-pulse rounded bg-muted" />
    </div>
  );
}

/** A panel calls this when its own data has arrived; until then the host's 2 s clock is running. */
const ReadyContext = createContext<() => void>(() => {});
export function useSlotReady(): () => void {
  return useContext(ReadyContext);
}

class SlotErrorBoundary extends Component<{ name: string; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(_error: Error, _info: ErrorInfo) {
    // The slot is optional UI: a broken contribution degrades to "unavailable".
  }
  render() {
    return this.state.failed ? <Unavailable name={this.props.name} /> : this.props.children;
  }
}

/**
 * Body of one `task.panel` tab: lazy, bounded by a 2 s timeout covering both the module
 * load and the panel's own first data request (it reports via `useSlotReady`), isolated
 * from the card on failure. A panel that becomes ready after the timeout replaces the placeholder.
 */
export function SlotPanelBody(props: { extensionId: string; name: string; task: Task }) {
  // Keyed per task: readiness and the 2 s timeout belong to one task's panel, a card
  // switched in place must start both over.
  return <SlotPanelBodyInner key={`${props.extensionId}:${props.task.id}`} {...props} />;
}

function SlotPanelBodyInner({ extensionId, name, task }: { extensionId: string; name: string; task: Task }) {
  const Panel = BUILTIN_SLOTS[extensionId]?.["task.panel"];
  const [ready, setReady] = useState(false);
  const [late, setLate] = useState(false);
  const markReady = useCallback(() => setReady(true), []);
  useEffect(() => {
    const id = setTimeout(() => setLate(true), SLOT_TIMEOUT_MS);
    return () => clearTimeout(id);
  }, []);
  if (!Panel) return <Unavailable name={name} />;
  return (
    <SlotErrorBoundary name={name} key={`${extensionId}:${task.id}`}>
      {late && !ready && <Unavailable name={name} />}
      <div className={late && !ready ? "hidden" : "flex min-h-0 flex-1 flex-col"}>
        <ReadyContext.Provider value={markReady}>
          <Suspense fallback={<Pending />}>
            <Panel task={task} />
          </Suspense>
        </ReadyContext.Provider>
      </div>
    </SlotErrorBoundary>
  );
}

/** The "…" menu of the task view header: `task.actions` items; renders nothing when there are none. */
export function SlotActionsMenu({ task, openPanel }: { task: Task; openPanel: (extensionId: string) => void }) {
  const { t } = useTranslation("task");
  const actions = useSlotActions();
  if (actions.length === 0) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className="flex items-center gap-1 rounded p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        aria-label={t("slots.actions.menu")}
        title={t("slots.actions.menu")}
        data-testid="slot-actions-trigger"
      >
        <MoreHorizontal className="h-4 w-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {actions.map((a) => (
          <DropdownMenuItem key={a.id} onClick={() => a.run({ task, openPanel })}>
            {t(a.labelKey, { name: a.extensionName })}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
