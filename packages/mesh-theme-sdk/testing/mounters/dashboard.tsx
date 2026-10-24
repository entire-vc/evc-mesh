// Containers for dashboard.layout and dashboard.widget.<id> (Mesh Doc slots-dashboard §4, §5). Dashboard views take
// `settings` and the dashboard ctx (`isMobile`, `wsSlug`) instead of `themeSettings` and the full SlotContext; the
// full context reaches them through the host Provider (`useSlotContext()`).
import type { ComponentType } from "react";
import type { DashboardLayoutData, DashboardSlotCtx, RealtimeEvent, Task, WidgetDescriptor, WidgetSize } from "../../src/index";
import { fixtures } from "../fixtures";
import type { CoreWidget, WidgetData } from "../fixtures/dashboard";
import type { ActionSpec, RenderEnv, Scenario, SlotMounter, Spies } from "../types";
import { registerMounter } from "./registry";

export type DashboardData = Omit<DashboardLayoutData, "components">;
export type { WidgetData };

const WS_SLUG = "ws"; // the host router's `:wsSlug`

const dashCtx = (env: RenderEnv<unknown>): DashboardSlotCtx => ({ isMobile: env.ctx.isMobile, wsSlug: WS_SLUG });

type DashView = ComponentType<{ data: unknown; actions: unknown; settings: Record<string, unknown>; ctx: DashboardSlotCtx }>;

/** What the core's `Widget` does with a size: the registry's min..max bound it (core `clampSize`). */
export function clamp(size: WidgetSize, d: WidgetDescriptor): WidgetSize {
  const c = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
  return { w: c(size.w, d.size.min.w, d.size.max.w) as WidgetSize["w"], h: c(size.h, d.size.min.h, d.size.max.h) as WidgetSize["h"] };
}

// -- dashboard.layout ----------------------------------------------------------------------------------

function layoutRender(env: RenderEnv<DashboardData>) {
  const known = [...env.data.widgets.map((w) => w.descriptor), ...env.data.available];
  // Stand-in for `components.Widget`: marks the place a widget is drawn, with the size it ends up with.
  const Widget = ({ id, size }: { id: string; size?: WidgetSize }) => {
    const d = known.find((x) => x.id === id);
    const s = d ? clamp(size ?? d.size.default, d) : size;
    return (
      <div data-mesh-widget={id} data-mesh-size={s ? `${s.w}x${s.h}` : ""}>
        {d?.title ?? id}
      </div>
    );
  };
  const View = env.view as unknown as DashView;
  return <View data={{ ...env.data, components: { Widget } }} actions={env.actions} settings={env.themeSettings} ctx={dashCtx(env as RenderEnv<unknown>)} />;
}

const layout: SlotMounter<DashboardData> = {
  slot: "dashboard.layout",
  defaults: (s) => fixtures.dashboard.dashboard({ widgets: s.themeSettings?.widgets as string[] | undefined }),
  actions: [{ name: "openTask" }, { name: "setWidgetOrder", needs: "edit", async: true }],
  render: layoutRender,
};
registerMounter(layout);

// -- dashboard.widget.<id> -----------------------------------------------------------------------------

const widgetId = (slot: string) => slot.slice("dashboard.widget.".length);

const WIDGET_ACTIONS: Record<CoreWidget, ActionSpec[]> = {
  mentions: [{ name: "openMention" }, { name: "retry" }],
  triage: [{ name: "openTask" }, { name: "retry" }],
  "my-tasks": [{ name: "openTask" }, { name: "quickEdit", needs: "edit", async: true }, { name: "retry" }],
  sessions: [{ name: "openAgent" }, { name: "retry" }],
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
for (const id of fixtures.dashboard.CORE_WIDGET_IDS) registerMounter(widgetMounter(`dashboard.widget.${id}`, WIDGET_ACTIONS[id]));
registerMounter(widgetMounter("dashboard.widget", []));
