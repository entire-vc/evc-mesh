// dashboard.layout / dashboard.widget.<id> hosts: the core builds data and
// actions, the theme (or mesh-default) only draws.
import { Component, type ComponentType, createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { CoreActionsProvider, useCoreActions } from "@/core/primitives";
import { roleCan, useCan } from "@/core/primitives/permissions";
import { SlotContextProvider } from "@/core/slots/slot-context";
import { resolveSlotOverride, useSlotRegistryVersion, useThemeSettings } from "@/core/slots/registry";
import { useBuildSlotContext } from "@/core/slots/use-slot-context";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { taskPath } from "@/core/primitives/actions";
import { useWidgetData } from "./hooks";
import { MoveDialog } from "./tasks/move-dialog";
import { WidgetConfigContext, type WidgetConfig } from "./config";
import { markMentionSeen } from "@/lib/mentions/inbox";
import { MENTION_SEEN_EVENT } from "./widgets/comms/hooks";
import { clearUnseenTs } from "./widgets/comms/storage";
import { descriptorFromV1, descriptorToV1, sizeToV1, toActionsV1, toLayoutV1 } from "@entire-vc/mesh-theme-sdk";
import { useLayoutMajor, useThemeWidgets, widgetSlot } from "./theme-widgets";
import i18n from "@/i18n";
import { WidgetError } from "./widgets/legacy/parts";
import { WIDGETS, clampSize, defaultInstances, loadWidgetView, peekWidgetView, resolveWidgets } from "./registry";
import type {
  DashboardLayoutActions,
  DashboardLayoutData,
  DashboardLayoutSettings,
  DashboardLayoutView,
  DashboardLayoutViewV1,
  MentionItem,
  SlotCtx,
  WidgetDescriptor,
  WidgetInstance,
  WidgetSizeV1,
} from "./types";
import type { Task } from "@/types";

/** What an active theme contributes to the dashboard (empty = mesh-default). */
export interface DashboardTheme {
  /** The layout view; `layoutMajor` says which contract it was written for (default: what the theme runtime recorded). */
  layout?: DashboardLayoutView | DashboardLayoutViewV1;
  layoutMajor?: number;
  /** Views by widget id: replaces a core view, or draws a theme's own widget. */
  widgets?: Record<string, AnyWidgetView>;
  /** Theme's own widgets (`theme.widgets[]`), listed next to the core ones. */
  extraWidgets?: WidgetDescriptor[];
  settings?: DashboardLayoutSettings;
}

// The host picks a view by id at run time, so per-id typing ends here.
type AnyWidgetView = ComponentType<any>;

const ThemeContext = createContext<DashboardTheme>({});
export function DashboardThemeProvider({ theme, children }: { theme: DashboardTheme; children: ReactNode }) {
  return <ThemeContext.Provider value={theme}>{children}</ThemeContext.Provider>;
}

class WidgetBoundary extends Component<{ fallback: ReactNode; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(error: unknown) {
    console.error("dashboard widget view failed", error);
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

const NO_CONFIG: WidgetConfig = {};
const DESKTOP_QUERY = "(min-width: 768px)"; // Tailwind `md`, the breakpoint the grid always switched on

function useIsMobile(): boolean {
  const [mobile, setMobile] = useState(() => !window.matchMedia(DESKTOP_QUERY).matches);
  useEffect(() => {
    const mq = window.matchMedia(DESKTOP_QUERY);
    const on = () => setMobile(!mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return mobile;
}

function useSlotCtx(): SlotCtx {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const isMobile = useIsMobile();
  return useMemo(() => ({ isMobile, wsSlug }), [isMobile, wsSlug]);
}

/** The full SlotContext the SDK's `useSlotContext()` returns inside a dashboard view. */
function useDashboardSlotContext(slot: string) {
  const ws = useWorkspaceStore((s) => s.currentWorkspace);
  return useBuildSlotContext(slot, ws ? { id: ws.id, slug: ws.slug, name: ws.name } : null);
}

function useWidgetActions(retry: () => void, loadMore?: () => void) {
  const navigate = useNavigate();
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const currentSlug = useProjectStore((s) => s.currentProject?.slug);
  const core = useCoreActions();
  return useMemo(() => {
    const go = (to: string, newTab?: boolean) => (newTab ? void window.open(to, "_blank", "noopener") : navigate(to));
    const openTask = (taskId: string, opts?: { newTab?: boolean }) =>
      opts?.newTab ? go(taskPath(wsSlug, currentSlug, taskId), true) : core.openTask(taskId);
    return {
      openTask,
      openMention: (m: MentionItem, opts?: { newTab?: boolean }) => {
        // «просмотрено» by click: the server first, then the widgets (list and counter) learn about it
        if (!m.seen) {
          markMentionSeen({ source: m.source, comment_id: m.id } as Parameters<typeof markMentionSeen>[0])
            .then(() => {
              clearUnseenTs();
              window.dispatchEvent(new CustomEvent(MENTION_SEEN_EVENT, { detail: { id: m.id } }));
            })
            .catch(() => {});
        }
        go(m.href ?? (wsSlug ? `/w/${wsSlug}/dashboard` : "/"), opts?.newTab);
      },
      openComment: (c: { href: string | null }, opts?: { newTab?: boolean }) => c.href && go(c.href, opts?.newTab),
      loadMore: () => loadMore?.(),
      // Agents live in Team now that the Sessions page is a dashboard template (§5.9).
      openAgent: () => navigate(wsSlug ? `/w/${wsSlug}/org-chart` : "/"),
      retry,
    };
  }, [retry, loadMore, navigate, wsSlug, currentSlug, core]);
}

/**
 * The group's view of a widget: known at once when it ships with the host or its views.tsx is already loaded,
 * otherwise loaded on mount (`view` stays undefined meanwhile). A failed load sets `error`; the widget's retry
 * remounts and loads again.
 */
function useWidgetView(id: string): { view?: AnyWidgetView | null; error?: Error } {
  const [state, setState] = useState<{ view?: AnyWidgetView | null; error?: Error }>(() => ({ view: peekWidgetView(id) }));
  useEffect(() => {
    if (state.view !== undefined) return;
    let live = true;
    loadWidgetView(id).then(
      (view) => live && setState({ view }),
      (error: Error) => live && setState({ error }),
    );
    return () => {
      live = false;
    };
  }, [id, state.view]);
  return state;
}

function WidgetInner({ instance, retry }: { instance: WidgetInstance; retry: () => void }) {
  const id = instance.type;
  const own = useWidgetView(id);
  const theme = useContext(ThemeContext);
  const ctx = useSlotCtx();
  const slotContext = useDashboardSlotContext(`dashboard.widget.${id}`);
  const runtimeSettings = useThemeSettings();
  useSlotRegistryVersion();
  const themeWidgets = useThemeWidgets();
  const descriptor = [...WIDGETS, ...(theme.extraWidgets ?? []), ...themeWidgets.map(descriptorFromV1)].find((d) => d.id === id);
  const result = useWidgetData(id, instance.settings);
  const base = useWidgetActions(retry, (result as { loadMore?: () => void } | undefined)?.loadMore);
  // moveTask (triage-queue): the core's move dialog, only for a widget that declares it and a role with task.move
  const canMove = useCan("task.move");
  const [moving, setMoving] = useState<Task | null>(null);
  const movable = canMove && !!descriptor?.actions.includes("moveTask");
  const actions = useMemo(() => {
    if (!movable) return base;
    const rows = result.state === "ready" ? ((result.value as { items?: Task[] } | undefined)?.items ?? []) : [];
    return { ...base, moveTask: (taskId: string) => setMoving(rows.find((t) => t.id === taskId) ?? null) };
  }, [base, movable, result]);
  if (!descriptor) return null;
  // The group's views.tsx is not here yet: hold the widget's place and draw nothing (its data is already on the way),
  // or, when the load failed, an error with a retry.
  const pending = own.view === undefined;
  const placeholder = (
    <div className="contents" data-mesh-widget={id} aria-busy={!own.error}>
      {own.error && <WidgetError error={new Error(i18n.t("notifications:dashboard.widgetLoadFailed"))} retry={retry} />}
    </div>
  );
  const size = clampSize(instance, descriptor);
  const settings = theme.settings ?? runtimeSettings;
  if (!descriptor.dataHook) {
    // A v2 widget: its group's view gets the instance, its own settings, its size and the core's actions.
    if (pending) return placeholder;
    const V2 = own.view;
    if (!V2) return null;
    return (
      <CoreActionsProvider actions={{ openTask: (taskId) => actions.openTask(taskId) }}>
        <div className="contents" data-mesh-widget={id}>
          <WidgetBoundary fallback={null}>
            <SlotContextProvider value={slotContext}>
              <V2 instance={instance} settings={instance.settings} size={size} actions={actions} />
            </SlotContextProvider>
          </WidgetBoundary>
        </div>
      </CoreActionsProvider>
    );
  }
  // A legacy widget (the core's four, a v1 theme's own): the v1 widget contract, unchanged. A theme's view does not
  // wait for the core one; the core one is its fallback once loaded.
  const data = { id, descriptor: descriptorToV1(descriptor), result, size: sizeToV1(size) };
  const Default = own.view ?? undefined;
  const themed = theme.widgets?.[id] ?? (resolveSlotOverride(widgetSlot(id)) as AnyWidgetView | undefined);
  if (!themed && pending) return placeholder;
  const View = themed ?? Default;
  if (!View) return null;
  const fallback = Default && View !== Default ? <Default data={data} actions={actions} settings={settings} ctx={ctx} /> : null;
  return (
    <CoreActionsProvider actions={{ openTask: (taskId) => actions.openTask(taskId) }}>
      <div className="contents" data-mesh-widget={id}>
        <WidgetBoundary fallback={fallback}>
          <SlotContextProvider value={slotContext}>
            <View data={data} actions={actions} settings={settings} ctx={ctx} />
          </SlotContextProvider>
        </WidgetBoundary>
        {/* the rows refresh on the move's task.* event */}
        {movable ? <MoveDialog task={moving} onClose={() => setMoving(null)} onMoved={() => {}} /> : null}
      </div>
    </CoreActionsProvider>
  );
}

/** One widget: data by id, the theme's view or mesh-default, isolated from its siblings' failures. */
export function Widget({ instance }: { instance: WidgetInstance }) {
  const [attempt, setAttempt] = useState(0);
  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  // The instance's own settings (limit, onlyNew, projectId, ...) reach the v1 data hooks through the config context.
  return (
    <WidgetConfigContext.Provider value={(instance.settings as WidgetConfig | undefined) ?? NO_CONFIG}>
      <WidgetInner key={`${instance.id}:${attempt}`} instance={instance} retry={retry} />
    </WidgetConfigContext.Provider>
  );
}

/** `components.Widget` of dashboard.layout 1.x: by descriptor id and a v1 size (columns x3 on the 12-column grid). */
export function WidgetV1({ id, size }: { id: string; size?: WidgetSizeV1 }) {
  const d = [...WIDGETS, ...useThemeWidgets().map(descriptorFromV1)].find((x) => x.id === id);
  const w = size ? size.w * 3 : d?.size.default.w ?? 6;
  const h = size ? size.h : d?.size.default.h ?? 2;
  return <Widget instance={{ id, type: id, x: 0, y: 0, w, h, settings: {} }} />;
}

export const MeshDefaultLayout: DashboardLayoutView = ({ data, ctx }) => {
  const { t } = useTranslation("notifications");
  const { Widget: W } = data.components;
  const ordered = [...data.widgets].sort((a, b) => a.instance.y - b.instance.y || a.instance.x - b.instance.x);
  if (ordered.length === 0) {
    return <p className="py-8 text-center text-sm text-muted-foreground">{t("dashboard.noWidgets", { defaultValue: "No widgets to show" })}</p>;
  }
  return (
    <div className={ctx.isMobile ? "grid grid-cols-1 gap-4" : "grid grid-cols-1 gap-4 md:grid-cols-2"}>
      {ordered.map((w) => (
        <W key={w.instance.id} instance={w.instance} />
      ))}
    </div>
  );
};

type PlacedWidget = { descriptor: WidgetDescriptor; instance: WidgetInstance };

/**
 * The dashboard.layout host shared by the legacy page and the coordinate page: the active theme's
 * layout, else a registered slot override, else `Default`; a crashing layout falls back to `Default`.
 * A `^1` theme gets the v1 data through the adapter. `place` puts the widgets on the grid (default:
 * the theme's `widgets` setting in the default arrangement).
 */
export function DashboardLayoutHost({
  Default,
  place,
  Widget: PageWidget,
}: {
  Default: DashboardLayoutView;
  place?: (registry: readonly WidgetDescriptor[]) => PlacedWidget[];
  /** `components.Widget` for this page (default: the core Widget); a `^1` theme reaches it by descriptor id. */
  Widget?: ComponentType<{ instance: WidgetInstance }>;
}) {
  const theme = useContext(ThemeContext);
  const ctx = useSlotCtx();
  const slotContext = useDashboardSlotContext("dashboard.layout");
  const role = useMemberStore((s) => s.myRole);
  const core = useCoreActions();
  const runtimeSettings = useThemeSettings();
  const runtimeMajor = useLayoutMajor();
  useSlotRegistryVersion();
  const themeWidgets = useThemeWidgets();
  const settings = theme.settings ?? runtimeSettings;
  const registry = useMemo(
    () => [...WIDGETS, ...(theme.extraWidgets ?? []), ...themeWidgets.map(descriptorFromV1)],
    [theme.extraWidgets, themeWidgets],
  );
  const data = useMemo<DashboardLayoutData>(() => {
    let widgets: PlacedWidget[];
    let available: WidgetDescriptor[] = [];
    if (place) widgets = place(registry);
    else {
      const r = resolveWidgets({ settings, registry, can: (p) => roleCan(role, p) });
      const instances = defaultInstances(r.shown, (settings as DashboardLayoutSettings).columns);
      widgets = r.shown.map((descriptor, i) => ({ descriptor, instance: instances[i]! }));
      available = r.available;
    }
    return { columns: 12, widgets, available, components: { Widget: PageWidget ?? Widget }, editing: false, canEdit: false };
  }, [settings, registry, role, place, PageWidget]);
  // v1 names a widget only by descriptor id: on a page, the second and later instances of a type get
  // "<type>:<instance id>" so each v1 cell (and its remove / expand / settings) stays its own instance.
  const v1Widgets = useMemo(() => {
    const seen = new Set<string>();
    return data.widgets.map((w) => {
      const id = w.descriptor.id;
      const dup = seen.has(id);
      seen.add(id);
      return dup && PageWidget ? { ...w, descriptor: { ...w.descriptor, id: `${id}:${w.instance.id}` } } : w;
    });
  }, [data.widgets, PageWidget]);
  const latest = useRef({ widgets: v1Widgets, W: data.components.Widget });
  latest.current = { widgets: v1Widgets, W: data.components.Widget };
  // One identity for the host's life (no remounts).
  const PageWidgetV1 = useRef(({ id }: { id: string }) => {
    const { widgets, W } = latest.current;
    const w = widgets.find((x) => x.descriptor.id === id);
    return w ? <W instance={w.instance} /> : null;
  }).current;
  const actions = useMemo<DashboardLayoutActions>(() => ({ openTask: (id) => core.openTask(id) }), [core]);
  const Layout = theme.layout ?? (resolveSlotOverride("dashboard.layout") as DashboardLayoutView | DashboardLayoutViewV1 | undefined) ?? Default;
  const major = Layout === Default ? 2 : (theme.layoutMajor ?? runtimeMajor);
  // dashboard.layout 1.x (ADR D6, one more release): the theme's view gets v1 data and no edit actions.
  const v1 = useMemo(() => (major === 1 ? { data: toLayoutV1({ ...data, widgets: v1Widgets }, PageWidget ? PageWidgetV1 : WidgetV1), actions: toActionsV1(actions) } : null), [major, data, v1Widgets, actions, PageWidget, PageWidgetV1]);
  const V1Layout = Layout as DashboardLayoutViewV1;
  const V2Layout = Layout as DashboardLayoutView;
  return (
    <WidgetBoundary fallback={<Default data={data} actions={actions} settings={settings} ctx={ctx} />}>
      <SlotContextProvider value={slotContext}>
        {v1 ? <V1Layout data={v1.data} actions={v1.actions} settings={settings} ctx={ctx} /> : <V2Layout data={data} actions={actions} settings={settings} ctx={ctx} />}
      </SlotContextProvider>
    </WidgetBoundary>
  );
}

export function DashboardContainer() {
  return <DashboardLayoutHost Default={MeshDefaultLayout} />;
}
