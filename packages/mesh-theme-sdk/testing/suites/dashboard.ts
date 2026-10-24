// CT-dash-01..06 and CT-widget-01..09 (Mesh Doc slots-dashboard §7).
// Not here: CT-dash-03 (a throwing widget falls back to mesh-default) is a core test, like CT-common-07: the host
// runs it, a theme cannot fail it. CT-widget-09 (a theme's own widget reads only SDK hooks) has no core widget to
// run on: CT-common-03 (no fetch) and CT-widget-01 (three states) already cover what a core widget can show.
import { fireEvent, waitFor } from "@testing-library/react";
import { expect } from "vitest";
import type { MentionItem, SessionItem, Task, WidgetSize } from "../../src/index";
import { fixtures } from "../fixtures";
import type { WidgetData } from "../fixtures/dashboard";
import { button, calls, markup, tick, visibleText } from "../helpers";
import { defineTests } from "../suite";
import type { Mounted } from "../types";
import type { DashboardData } from "../mounters/dashboard";

const F = fixtures.dashboard;
const LAYOUT = ["dashboard.layout"];
const ALL_WIDGETS = ["dashboard.widget.*"];

const shownIds = (root: ParentNode) => Array.from(root.querySelectorAll("[data-mesh-widget]")).map((e) => e.getAttribute("data-mesh-widget"));

/** Widgets data with `result` replaced (ready value, loading, error). */
const withResult = (result: unknown) => (d: WidgetData): WidgetData => ({ ...d, result: result as WidgetData["result"] });
const readyValue = <T>(d: WidgetData) => (d.result as { state: "ready"; value: T }).value;

/** The innermost element whose own text contains `text`. */
function byText(root: Element, text: string): HTMLElement | null {
  const all = Array.from(root.querySelectorAll<HTMLElement>("*")).filter((e) => e.textContent?.includes(text));
  return all.find((e) => !Array.from(e.children).some((c) => c.textContent?.includes(text))) ?? null;
}

/** Clicks `el` and reports whether the view prevented the default (an in-app open) or left it to the browser. */
function clickAndSee(el: Element, init: MouseEventInit = {}): { prevented: boolean } {
  // `dispatchEvent` returns false when a handler cancelled the event (a link may stop propagation, so a listener
  // higher up would never see it). A click left to the browser makes jsdom log "navigation not implemented".
  return { prevented: !fireEvent.click(el, { button: 0, ...init }) };
}

const realHref = (a: Element | null) => {
  const href = a?.getAttribute("href") ?? "";
  return href.length > 0 && href !== "#" && !href.startsWith("javascript:");
};

/** Inline sizes a 1-column widget cannot afford (jsdom has no layout: what the markup itself pins down). */
function pinnedWide(root: Element): string[] {
  const out: string[] = [];
  root.querySelectorAll<HTMLElement>("[style]").forEach((e) => {
    for (const prop of ["width", "minWidth"] as const) {
      const v = e.style[prop];
      const px = /^(\d+(?:\.\d+)?)px$/.exec(v);
      if (px && Number(px[1]) > 320) out.push(`${prop}:${v}`);
      if (/^\d+(\.\d+)?vw$/.test(v) && parseFloat(v) > 100) out.push(`${prop}:${v}`);
    }
  });
  return out;
}

defineTests([
  // ---------------------------------------------------------------- dashboard.layout
  {
    id: "CT-dash-01",
    title: "draws exactly data.widgets, in their order, each through components.Widget",
    slots: LAYOUT,
    run({ mount }) {
      const m = mount<DashboardData>({ data: F.dashboard({ widgets: ["triage", "my-tasks"] }), themeSettings: { widgets: ["triage", "my-tasks"] } });
      expect(shownIds(m.container)).toEqual(["triage", "my-tasks"]);
      m.unmount();
      const r = mount<DashboardData>({ data: F.dashboard({ widgets: ["sessions", "mentions"] }) });
      expect(shownIds(r.container), "the view draws data.widgets, not its own set").toEqual(["sessions", "mentions"]);
    },
  },
  {
    id: "CT-dash-02",
    title: "no widgets: an empty state with a reason, not a blank field",
    slots: LAYOUT,
    run({ mount }) {
      const m = mount<DashboardData>({ data: F.dashboard({ widgets: [] }), themeSettings: { widgets: [] } });
      expect(shownIds(m.container)).toEqual([]);
      expect(visibleText(m.container).trim().length, "an empty dashboard must say why").toBeGreaterThan(0);
    },
  },
  {
    id: "CT-dash-04",
    title: "isMobile: every widget kept, in order, nothing pinned to more than one column",
    slots: LAYOUT,
    run({ mount }) {
      const ids = [...F.CORE_WIDGET_IDS];
      const desk = mount<DashboardData>();
      expect(shownIds(desk.container)).toEqual(ids);
      desk.unmount();
      const m = mount<DashboardData>({ isMobile: true });
      expect(shownIds(m.container), "no widget dropped, order kept").toEqual(ids);
      // jsdom has no layout: what can be checked is an inline grid the view sets for the phone.
      m.container.querySelectorAll<HTMLElement>("[style]").forEach((e) => {
        const cols = e.style.gridTemplateColumns;
        if (cols) expect(cols.trim().split(/\s+/).length, `grid-template-columns: ${cols}`).toBe(1);
        if (e.style.columnCount) expect(Number(e.style.columnCount)).toBeLessThanOrEqual(1);
      });
    },
  },
  {
    id: "CT-dash-05",
    title: "a size outside the registry's min..max ends up drawn within it",
    slots: LAYOUT,
    run({ mount }) {
      const tooBig: WidgetSize = { w: 9 as WidgetSize["w"], h: 9 as WidgetSize["h"] };
      const tooSmall: WidgetSize = { w: 0 as WidgetSize["w"], h: 0 as WidgetSize["h"] };
      const m = mount<DashboardData>({
        data: (d) => ({ ...d, widgets: d.widgets.map((w, i) => ({ ...w, size: i % 2 ? tooSmall : tooBig })) }),
      });
      const data = m.data();
      expect(shownIds(m.container)).toEqual(data.widgets.map((w) => w.descriptor.id));
      for (const w of data.widgets) {
        const el = m.container.querySelector(`[data-mesh-widget="${w.descriptor.id}"]`)!;
        const [sw, sh] = (el.getAttribute("data-mesh-size") ?? "").split("x").map(Number);
        const { min, max } = w.descriptor.size;
        expect(sw! >= min.w && sw! <= max.w && sh! >= min.h && sh! <= max.h, `${w.descriptor.id} drawn ${sw}x${sh}`).toBe(true);
      }
      // A span the view sets itself must stay within the registry too.
      m.container.querySelectorAll<HTMLElement>("[style]").forEach((e) => {
        const span = /span\s+(\d+)/.exec(e.style.gridColumn || e.style.gridColumnEnd || "");
        if (span) expect(Number(span[1])).toBeLessThanOrEqual(4);
      });
    },
  },
  {
    id: "CT-dash-06",
    title: "an extension's widget in the registry is drawn; the layout does not fail on an unknown id",
    slots: LAYOUT,
    run({ mount }) {
      const ext = F.descriptor("gitlab.pipelines", { origin: "extension", title: "Pipelines" });
      const own = F.descriptor("ink.unknown-to-the-view", { origin: "theme" });
      const m = mount<DashboardData>({ data: F.dashboard({ widgets: [F.descriptor("mentions"), ext, own, F.descriptor("sessions")] }) });
      expect(shownIds(m.container)).toEqual(["mentions", "gitlab.pipelines", "ink.unknown-to-the-view", "sessions"]);
    },
  },

  // ---------------------------------------------------------------- dashboard.widget.<id>
  {
    id: "CT-widget-01",
    title: "loading, error and empty are three different views; retry is drawn and called only in error",
    slots: ALL_WIDGETS,
    async run({ mount }) {
      const probe = mount<WidgetData>();
      const ready = probe.data().result;
      probe.unmount();
      if (ready === undefined) return; // a theme's own widget: its states come from hooks (CT-widget-09)
      const emptyValue = { ...(ready as { value: object }).value, items: [], total: 0, failedSources: [] };
      const views: Record<string, Mounted<WidgetData>> = {
        loading: mount<WidgetData>({ options: { state: "loading" } }),
      };
      const html: Record<string, string> = { loading: markup(views.loading!.container) };
      const retryIn: Record<string, boolean> = { loading: button(views.loading!.container, /retry|повтор|try again/i) !== null };
      views.loading!.unmount();
      const empty = mount<WidgetData>({ data: withResult({ state: "ready", value: emptyValue }) });
      html.empty = markup(empty.container);
      retryIn.empty = button(empty.container, /retry|повтор|try again/i) !== null;
      empty.unmount();
      const error = mount<WidgetData>({ options: { state: "error" } });
      html.error = markup(error.container);
      expect(new Set(Object.values(html)).size, "loading / error / empty must look different").toBe(3);
      expect(retryIn, "retry is only in the error state").toEqual({ loading: false, empty: false });
      const retry = button(error.container, /retry|повтор|try again/i);
      expect(retry, "the error state needs a retry").not.toBeNull();
      await error.user.click(retry!);
      expect(calls(error, "retry").length).toBeGreaterThan(0);
    },
  },
  {
    id: "CT-widget-02",
    title: "mentions: a source that failed to load is visible, not shown as 'no mentions'",
    slots: ["dashboard.widget.mentions"],
    run({ mount }) {
      // Scenario of the spec table: the document inbox failed, the task inbox answered with nothing.
      const ok = mount<WidgetData>({ data: withResult({ state: "ready", value: { items: [], failedSources: [] } }) });
      const quiet = visibleText(ok.container);
      ok.unmount();
      const failed = mount<WidgetData>({ data: withResult({ state: "ready", value: { items: [], failedSources: ["document"] } }) });
      expect(visibleText(failed.container), "a failed source reads differently from an empty inbox").not.toEqual(quiet);
    },
  },
  {
    id: "CT-widget-03",
    title: "mentions: click opens the mention with its target; Ctrl+click opens a new tab",
    slots: ["dashboard.widget.mentions"],
    run({ mount }) {
      const m = mount<WidgetData>();
      const items = readyValue<{ items: MentionItem[] }>(m.data()).items;
      expect(items.map((i) => i.target.kind).sort()).toEqual(["document", "task"]);
      for (const item of items) {
        const el = byText(m.container, item.title);
        expect(el, `mention "${item.title}" is drawn`).not.toBeNull();
        m.spies.openMention!.mockClear();
        clickAndSee(el!);
        const plain = calls(m, "openMention");
        expect(plain.length, `plain click on ${item.target.kind} mention`).toBe(1);
        expect((plain[0]![0] as MentionItem).id).toBe(item.id);
        expect((plain[0]![0] as MentionItem).target).toEqual(item.target);
        m.spies.openMention!.mockClear();
        const { prevented } = clickAndSee(el!, { ctrlKey: true });
        const modified = calls(m, "openMention");
        // Either the view asks for a new tab, or it leaves the click to the browser on a real link to the mention.
        const newTab = modified.length === 1 && (modified[0]![1] as { newTab?: boolean } | undefined)?.newTab === true;
        const link = el!.closest("a");
        const toBrowser = modified.length === 0 && !prevented && realHref(link) && link!.getAttribute("href") === item.href;
        expect(newTab || toBrowser, `Ctrl+click on ${item.target.kind} mention: openMention(item, {newTab:true}) or a real link`).toBe(true);
      }
    },
  },
  {
    id: "CT-widget-04",
    title: "task rows: a real href; a link to the task opens it through openTask on a plain click",
    slots: ["dashboard.widget.triage", "dashboard.widget.my-tasks"],
    run({ mount }) {
      const m = mount<WidgetData>();
      const items = readyValue<{ items: Task[] }>(m.data()).items;
      expect(items.length).toBeGreaterThan(0);
      let taskLinks = 0;
      for (const t of items) {
        const el = byText(m.container, t.title);
        expect(el, `row "${t.title}" is drawn`).not.toBeNull();
        const a = el!.closest("a");
        expect(realHref(a), `row "${t.title}" is a real link (new tab, copy link)`).toBe(true);
        if (!a!.getAttribute("href")!.includes(t.id)) continue; // a row may open a list page (triage) instead of the task
        taskLinks++;
        m.spies.openTask!.mockClear();
        const { prevented } = clickAndSee(el!);
        expect(calls(m, "openTask").map((c) => c[0]), `plain click on "${t.title}"`).toEqual([t.id]);
        expect(prevented, "an in-app open, not a page load").toBe(true);
        m.spies.openTask!.mockClear();
        expect(clickAndSee(el!, { ctrlKey: true }).prevented, "Ctrl+click is left to the browser").toBe(false);
        expect(calls(m, "openTask")).toEqual([]);
      }
      if (m.data().id === "my-tasks") expect(taskLinks, "my-tasks rows link to their task").toBe(items.length);
    },
  },
  {
    id: "CT-widget-05",
    title: "my-tasks: a role without task.edit gets no inline editors and no quickEdit",
    slots: ["dashboard.widget.my-tasks"],
    async run({ mount }) {
      const m = mount<WidgetData>({ role: "viewer" });
      await tick();
      expect(m.spies.quickEdit, "quickEdit is absent from the viewer's actions").toBeUndefined();
      const editors = m.container.querySelectorAll("input,select,textarea,[contenteditable=true],[role=combobox],[role=textbox]");
      expect(editors.length, "no editors in place for a viewer").toBe(0);
    },
  },
  {
    id: "CT-widget-06",
    title: "my-tasks: a realtime update brings a new result; the list changes without remounting the widget",
    slots: ["dashboard.widget.my-tasks"],
    async run({ mount }) {
      const m = mount<WidgetData>();
      const first = readyValue<{ items: Task[] }>(m.data()).items[0]!;
      const root = m.container.firstElementChild;
      expect(root).not.toBeNull();
      await m.emitRealtime({ type: "task.updated", data: { id: first.id, title: "Renamed live" }, at: "1" });
      await waitFor(() => expect(visibleText(m.container)).toContain("Renamed live"));
      expect(visibleText(m.container)).not.toContain(first.title);
      expect(m.container.firstElementChild, "same widget element: updated, not remounted").toBe(root);
    },
  },
  {
    id: "CT-widget-07",
    title: "sessions: a busy agent shows its status and what it works on; clicking the agent calls openAgent(id)",
    slots: ["dashboard.widget.sessions"],
    run({ mount }) {
      const busy = F.agent("a9", { name: "Liam", heartbeat_status: "busy", heartbeat_message: "Working on Fix login", current_tasks: 1 });
      const m = mount<WidgetData>({ data: withResult({ state: "ready", value: { items: [F.session(busy, "busy")] satisfies SessionItem[] } }) });
      const text = visibleText(m.container);
      expect(text).toContain("Liam");
      expect(text.toLowerCase(), "the status is shown").toMatch(/busy|занят/);
      expect(text, "the current work is shown").toContain("Working on Fix login");
      const name = byText(m.container, "Liam")!;
      const control = name.closest("a[href],button,[role=button],[role=link]");
      if (!control) return; // the row is not interactive: nothing claims to open the agent
      clickAndSee(control);
      const opened = calls(m, "openAgent");
      expect(opened.length > 0 ? opened[0]![0] : realHref(control) ? "a9" : null, "the agent's control opens that agent").toBe("a9");
    },
  },
  {
    id: "CT-widget-08",
    title: "size.w = 1: nothing is dropped and nothing pins the widget wider than a narrow column",
    slots: ALL_WIDGETS,
    run({ mount }) {
      const m = mount<WidgetData>({ data: (d) => ({ ...d, size: { w: 1, h: d.size.h } }) });
      expect(m.container.innerHTML.length).toBeGreaterThan(0);
      const r = m.data().result;
      if (r?.state === "ready") {
        const text = visibleText(m.container);
        const items = (r.value as { items: Array<{ title?: string; agent?: { name: string } }> }).items;
        for (const it of items) expect(text, "every row is still there at w=1").toContain(it.title ?? it.agent!.name);
      }
      expect(pinnedWide(m.container), "inline widths a 1-column widget cannot fit").toEqual([]);
    },
  },
]);
