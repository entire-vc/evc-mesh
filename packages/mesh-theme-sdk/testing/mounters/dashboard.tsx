// Containers for dashboard.layout and dashboard.widget.<id> (Mesh Doc slots-dashboard §4, §5). Dashboard views take
// `settings` and the dashboard ctx (`isMobile`, `wsSlug`) instead of `themeSettings` and the full SlotContext; the
// full context reaches them through the host Provider (`useSlotContext()`).
import type { ComponentType } from "react";
import type { DashboardLayoutData, DashboardLayoutDataV1, DashboardSlotCtx, RealtimeEvent, Task, WidgetDescriptor, WidgetInstance, WidgetSize } from "../../src/index";
import { fixtures } from "../fixtures";
import type { CoreWidget, WidgetData } from "../fixtures/dashboard";
import type { ActionSpec, RenderEnv, Scenario, SlotMounter, Spies } from "../types";
import { registerMounter } from "./registry";

export type DashboardData = Omit<DashboardLayoutData, "components">;
/** dashboard.layout 1.x: the mounter serves it when `options.contract` is 1 (the default). */
export type DashboardDataV1 = Omit<DashboardLayoutDataV1, "components">;
export type { WidgetData };

const WS_SLUG = "ws"; // the host router's `:wsSlug`

const dashCtx = (env: RenderEnv<unknown>): DashboardSlotCtx => ({ isMobile: env.ctx.isMobile, wsSlug: WS_SLUG });

type DashView = ComponentType<{ data: unknown; actions: unknown; settings: Record<string, unknown>; ctx: DashboardSlotCtx }>;

/** What the core's `Widget` does with a size: the registry's min..max bound it (core `clampSize`). */
export function clamp(size: { w: number; h: number }, d: WidgetDescriptor): WidgetSize {
  const c = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
  return { w: c(size.w, d.size.min.w, d.size.max.w) as WidgetSize["w"], h: c(size.h, d.size.min.h, d.size.max.h) as WidgetSize["h"] };
}

// -- dashboard.layout ---------------------------------------------------------------------------------
// Two live majors (ADR D6): 2 draws instances on the 12-column grid, 1 the v1 shape. `options.contract` picks.

const contractOf = (s: { options?: Record<string, unknown> }) => (s.options?.contract === 2 ? 2 : 1);

function layoutRender(env: RenderEnv<DashboardData | DashboardDataV1>) {
  const View = env.view as unknown as DashView;
  const known = [...env.data.widgets.map((w) => w.descriptor), ...env.data.available] as Pick<WidgetDescriptor, "id" | "title" | "size">[];
  if (contractOf(env.scenario) === 2) {
    // Stand-in for `components.Widget`: marks the place a widget is drawn, with its cell and the size it ends up with.
    const Widget = ({ instance }: { instance: WidgetInstance }) => {
      const d = (known as WidgetDescriptor[]).find((x) => x.id === instance.type);
      const s = d ? clamp(instance, d) : instance;
      return (
        <div data-mesh-widget={instance.type} data-mesh-instance={instance.id} data-mesh-cell={`${instance.x},${instance.y}`} data-mesh-size={`${s.w}x${s.h}`}>
          {d?.title ?? instance.type}
        </div>
      );
    };
    return <View data={{ ...env.data, components: { Widget } }} actions={env.actions} settings={env.themeSettings} ctx={dashCtx(env as RenderEnv<unknown>)} />;
  }
  // v1 stand-in: a v1 size is 1..4 columns, 1..3 rows.
  const Widget = ({ id, size }: { id: string; size?: { w: number; h: number } }) => {
    const d = (known as unknown as { id: string; title: string; size: { default: { w: number; h: number }; min: { w: number; h: number }; max: { w: number; h: number } } }[]).find((x) => x.id === id);
    const s = d ? clamp((size ?? d.size.default) as WidgetSize, d as unknown as WidgetDescriptor) : size;
    return (
      <div data-mesh-widget={id} data-mesh-size={s ? `${s.w}x${s.h}` : ""}>
        {d?.title ?? id}
      </div>
    );
  };
  return <View data={{ ...env.data, components: { Widget } }} actions={env.actions} settings={env.themeSettings} ctx={dashCtx(env as RenderEnv<unknown>)} />;
}

const layout: SlotMounter<DashboardData | DashboardDataV1> = {
  slot: "dashboard.layout",
  defaults: (s) => {
    const widgets = s.themeSettings?.widgets as string[] | undefined;
    return contractOf(s) === 2 ? fixtures.dashboard.dashboard({ widgets }) : fixtures.dashboard.dashboardV1({ widgets });
  },
  actions: [
    { name: "openTask" },
    // v1: setWidgetOrder. v2: the edit actions, which exist only when `canEdit`.
    { name: "setWidgetOrder", needs: "edit", async: true },
    { name: "moveWidget", needs: "edit", async: true },
    { name: "resizeWidget", needs: "edit", async: true },
    { name: "addWidget", needs: "edit", async: true },
    { name: "removeWidget", needs: "edit", async: true },
    { name: "updateWidgetSettings", needs: "edit", async: true },
  ],
  render: layoutRender,
};
registerMounter(layout as unknown as SlotMounter<unknown>);

// -- dashboard.widget.<id> -----------------------------------------------------------------------------

const widgetId = (slot: string) => slot.slice("dashboard.widget.".length);

const WIDGET_ACTIONS: Record<CoreWidget, ActionSpec[]> = {
  mentions: [{ name: "openMention" }, { name: "retry" }],
  triage: [{ name: "openTask" }, { name: "retry" }],
  "my-tasks": [{ name: "openTask" }, { name: "quickEdit", needs: "edit", async: true }, { name: "retry" }],
  sessions: [{ name: "openAgent" }, { name: "retry" }],
  "triage-queue": [{ name: "openTask" }, { name: "moveTask", needs: "move" }, { name: "retry" }],
  "task-list": [{ name: "openTask" }, { name: "quickEdit", needs: "edit", async: true }, { name: "retry" }],
  "my-comments": [{ name: "openComment" }, { name: "loadMore" }, { name: "retry" }],
  "recent-comments": [{ name: "openComment" }, { name: "loadMore" }, { name: "retry" }],
};

function widgetRender(env: RenderEnv<WidgetData>) {
  const View = env.view as unknown as DashView;
  return <View data={env.data} actions={env.actions} settings={env.themeSettings} ctx={dashCtx(env as RenderEnv<unknown>)} />;
}

/** A store update for a task the widget lists: a new `result`, the rest keeps its identity. */
function widgetReduce(data: WidgetData, e: RealtimeEvent): WidgetData {
  const r = data.result;
  if (e.type !== "task.updated" || r?.state !== "ready" || !("total" in r.value)) return data;
  const { id, title } = e.data as { id: string; title?: string };
  if (!title || !r.value.items.some((t) => t.id === id)) return data;
  const items = r.value.items.map((t): Task => (t.id === id ? { ...t, title } : t));
  return { ...data, result: { state: "ready", value: { ...r.value, items } } } as WidgetData;
}

function widgetAsync(data: WidgetData, state: string, spies: Spies): WidgetData {
  if (data.result === undefined) return data; // a theme's own widget reads hooks, it has no `result`
  if (state === "loading") return { ...data, result: { state: "loading" } };
  if (state === "error") return { ...data, result: { state: "error", error: new Error(`Could not load ${data.id}`), retry: () => void spies.retry?.() } };
  return data;
}

function widgetMounter(slot: string, actions: ActionSpec[]): SlotMounter<WidgetData> {
  return {
    slot,
    defaults: (s: Scenario<WidgetData>) => fixtures.dashboard.widget(widgetId(s.slot)),
    actions,
    reduce: widgetReduce,
    render: widgetRender,
    asyncData: widgetAsync,
  };
}

// A core id gets its own action set; any other id (a theme's own widget) gets no actions (`Record<string, never>`).
for (const id of fixtures.dashboard.CORE_WIDGET_IDS) {
  registerMounter(widgetMounter(`dashboard.widget.${id}`, WIDGET_ACTIONS[id]));
}
registerMounter(widgetMounter("dashboard.widget", []));
