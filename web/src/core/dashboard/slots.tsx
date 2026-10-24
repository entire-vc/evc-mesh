// dashboard.layout / dashboard.widget.<id> hosts: the core builds data and
// actions, the theme (or mesh-default) only draws.
import { Component, type ComponentType, createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { CoreActionsProvider, useCoreActions } from "@/core/primitives";
import { roleCan } from "@/core/primitives/permissions";
import { SlotContextProvider } from "@/core/slots/slot-context";
import { resolveSlotOverride, useSlotRegistryVersion, useThemeSettings } from "@/core/slots/registry";
import { useBuildSlotContext } from "@/core/slots/use-slot-context";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { useMemberStore } from "@/stores/member";
import { taskPath } from "@/core/primitives/actions";
import { useWidgetData } from "./hooks";
import { useThemeWidgets, widgetSlot } from "./theme-widgets";
import { CORE_WIDGETS, clampSize, resolveWidgets } from "./registry";
import { MESH_DEFAULT_WIDGETS } from "./views/mesh-default";
import type {
  DashboardLayoutActions,
  DashboardLayoutData,
  DashboardLayoutSettings,
  DashboardLayoutView,
  SlotCtx,
  WidgetDescriptor,
  WidgetSize,
} from "./types";

/** What an active theme contributes to the dashboard (empty = mesh-default). */
export interface DashboardTheme {
  layout?: DashboardLayoutView;
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

function useWidgetActions(retry: () => void) {
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
      openMention: (m: { href: string | null }, opts?: { newTab?: boolean }) =>
        go(m.href ?? (wsSlug ? `/w/${wsSlug}/activity` : "/"), opts?.newTab),
      openAgent: () => navigate(wsSlug ? `/w/${wsSlug}/sessions` : "/"),
      retry,
    };
  }, [retry, navigate, wsSlug, currentSlug, core]);
}

function WidgetInner({ id, size, retry }: { id: string; size?: WidgetSize; retry: () => void }) {
  const theme = useContext(ThemeContext);
  const ctx = useSlotCtx();
  const slotContext = useDashboardSlotContext(`dashboard.widget.${id}`);
  const runtimeSettings = useThemeSettings();
  useSlotRegistryVersion();
  const themeWidgets = useThemeWidgets();
  const descriptor = [...CORE_WIDGETS, ...(theme.extraWidgets ?? []), ...themeWidgets].find((d) => d.id === id);
  const result = useWidgetData(id);
  const actions = useWidgetActions(retry);
  if (!descriptor) return null;
  const data = { id, descriptor, result, size: clampSize(size ?? descriptor.size.default, descriptor) };
  const settings = theme.settings ?? runtimeSettings;
  const Default = (MESH_DEFAULT_WIDGETS as Record<string, AnyWidgetView>)[id];
  const View = theme.widgets?.[id] ?? (resolveSlotOverride(widgetSlot(id)) as AnyWidgetView | undefined) ?? Default;
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
      </div>
    </CoreActionsProvider>
  );
}

/** One widget: data by id, the theme's view or mesh-default, isolated from its siblings' failures. */
export function Widget({ id, size }: { id: string; size?: WidgetSize }) {
  const [attempt, setAttempt] = useState(0);
  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  return <WidgetInner key={`${id}:${attempt}`} id={id} size={size} retry={retry} />;
}

export const MeshDefaultLayout: DashboardLayoutView = ({ data, ctx }) => {
  const { t } = useTranslation("notifications");
  const { Widget: W } = data.components;
  if (data.widgets.length === 0) {
    return <p className="py-8 text-center text-sm text-muted-foreground">{t("dashboard.noWidgets", { defaultValue: "No widgets to show" })}</p>;
  }
  return (
    <div className={ctx.isMobile ? "grid grid-cols-1 gap-4" : "grid grid-cols-1 gap-4 md:grid-cols-2"}>
      {data.widgets.map((w) => (
        <W key={w.descriptor.id} id={w.descriptor.id} size={w.size} />
      ))}
    </div>
  );
};

export function DashboardContainer() {
  const theme = useContext(ThemeContext);
  const ctx = useSlotCtx();
  const slotContext = useDashboardSlotContext("dashboard.layout");
  const role = useMemberStore((s) => s.myRole);
  const core = useCoreActions();
  const runtimeSettings = useThemeSettings();
  useSlotRegistryVersion();
  const themeWidgets = useThemeWidgets();
  const settings = theme.settings ?? runtimeSettings;
  const registry = useMemo(() => [...CORE_WIDGETS, ...(theme.extraWidgets ?? []), ...themeWidgets], [theme.extraWidgets, themeWidgets]);
  const data = useMemo<DashboardLayoutData>(() => {
    const { shown, available } = resolveWidgets({ settings, registry, can: (p) => roleCan(role, p) });
    return {
      widgets: shown.map((descriptor) => ({ descriptor, size: descriptor.size.default, hidden: false as const })),
      available,
      components: { Widget },
      editing: false,
    };
  }, [settings, registry, role]);
  const actions = useMemo<DashboardLayoutActions>(() => ({ openTask: (id) => core.openTask(id) }), [core]);
  const Layout = theme.layout ?? (resolveSlotOverride("dashboard.layout") as DashboardLayoutView | undefined) ?? MeshDefaultLayout;
  return (
    <WidgetBoundary fallback={<MeshDefaultLayout data={data} actions={actions} settings={settings} ctx={ctx} />}>
      <SlotContextProvider value={slotContext}>
        <Layout data={data} actions={actions} settings={settings} ctx={ctx} />
      </SlotContextProvider>
    </WidgetBoundary>
  );
}
