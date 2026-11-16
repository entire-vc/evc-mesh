import { describe, expect, it, vi } from "vitest";
import { descriptorFromV1, toActionsV1, toLayoutV1, type DashboardLayoutData } from "@entire-vc/mesh-theme-sdk";
import { CATALOG_ONLY, DEFAULT_CORE_WIDGET_IDS, WIDGETS, buildRegistry, createViewCache, defaultInstances, loadWidgetView, peekWidgetView, resolveWidgets, type WidgetViewsLoader } from "../registry";

const ANALYTICS = ["metric", "tasks-breakdown", "tasks-timeline", "events-breakdown", "agent-leaderboard", "cost-kpis", "cost-by-agent", "cost-top-tasks", "cost-by-project", "cost-by-day"];

type Groups = Parameters<typeof buildRegistry>[0];
const fixtureGroups = import.meta.glob("./fixtures/widgets/newgroup/index.ts", { eager: true }) as Groups;
const fixtureViews = import.meta.glob("./fixtures/widgets/newgroup/views.tsx") as Record<string, WidgetViewsLoader>;
const dupGroups = import.meta.glob("./fixtures/widgets/dup/index.ts", { eager: true }) as Groups;
const eagerGroups = import.meta.glob("./fixtures/widgets/eagerviews/index.ts", { eager: true }) as Groups;
const legacyGroup = import.meta.glob("../widgets/legacy/index.ts", { eager: true }) as Groups;
/** The pre-registry widgets still in the legacy group (my-tasks moved to the tasks group). */
const LEGACY = buildRegistry(legacyGroup, true).descriptors;
/** The default dashboard (PRD §5.3): the four it always had, in their old order; the layout tests are about them. */
const DEFAULTS = DEFAULT_CORE_WIDGET_IDS.map((id) => WIDGETS.find((d) => d.id === id)!);

describe("widget registry (folders)", () => {
  it("the real glob finds the legacy group first, then the other groups by folder, each with a view", async () => {
    expect(LEGACY.map((d) => d.id)).toEqual(["mentions", "triage", "sessions"]);
    // The default dashboard (PRD 5.3) is the old four: the legacy group plus my-tasks, now in the tasks group.
    expect([...DEFAULT_CORE_WIDGET_IDS]).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    expect(WIDGETS.map((d) => d.id)).toEqual([
      "mentions", "triage", "sessions", "agents-active", "agents-status", "agents-summary", ...ANALYTICS, "my-comments", "recent-comments", "event-feed", "initiatives", "initiative", "my-tasks", "triage-queue", "task-list",
    ]);
    for (const id of ["mentions", "triage", "sessions", "my-comments", "recent-comments"]) {
      expect(typeof peekWidgetView(id), id).toBe("function"); // legacy and comms views ship with the host: no load
    }
    expect(peekWidgetView("event-feed")).toBeUndefined(); // a lazy group: unknown until its views.tsx loads
    expect(peekWidgetView("agents-active")).toBeUndefined();
    expect(peekWidgetView("metric")).toBeUndefined(); // analytics too
    expect(peekWidgetView("my-tasks")).toBeUndefined(); // the tasks group is lazy too
    for (const d of WIDGETS) {
      expect(typeof (await loadWidgetView(d.id)), d.id).toBe("function"); // other groups: their views.tsx, on demand
      expect(peekWidgetView(d.id)).toBe(await loadWidgetView(d.id));
    }
    expect(peekWidgetView("no-such-widget")).toBeNull();
  });

  it("a catalog-only group's widget is in the catalog, not on a dashboard that picks no widgets; picked, it shows", () => {
    expect([...CATALOG_ONLY]).toEqual([...ANALYTICS, "event-feed", "initiatives", "initiative"]);
    const none = resolveWidgets({});
    // no theme setting: the default dashboard is the four it always had, in their old order (PRD §5.3)
    expect(none.shown.map((d) => d.id)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    expect(none.available.map((d) => d.id)).toEqual(["agents-active", "agents-status", "agents-summary", ...ANALYTICS, "my-comments", "recent-comments", "event-feed", "initiatives", "initiative", "triage-queue", "task-list"]);
    const picked = resolveWidgets({ settings: { widgets: ["event-feed", "mentions"] } });
    expect(picked.shown.map((d) => d.id)).toEqual(["event-feed", "mentions"]);
    // a non-core widget shows by default unless its id is catalog-only (positive control: the empty set)
    const extra = { ...WIDGETS[0]!, id: "ink.feed", origin: "theme" as const };
    expect(resolveWidgets({ registry: [...WIDGETS, extra], catalogOnly: new Set(["ink.feed"]) }).shown.map((d) => d.id)).not.toContain("ink.feed");
    expect(resolveWidgets({ registry: [...WIDGETS, extra], catalogOnly: new Set() }).shown.map((d) => d.id)).toContain("ink.feed");
    // the real catalog-only set also keeps out a widget the default list alone would show (a non-core one)
    const feed = WIDGETS.find((d) => d.id === "event-feed")!;
    const own = [{ ...feed, origin: "extension" as const }];
    expect(resolveWidgets({ registry: own, catalogOnly: CATALOG_ONLY }).shown).toEqual([]);
    expect(resolveWidgets({ registry: own, catalogOnly: new Set() }).shown.map((d) => d.id)).toEqual(["event-feed"]);
  });

  it("a preloaded view is known without calling the group's loader", async () => {
    const View = () => null;
    const load = vi.fn<WidgetViewsLoader>();
    const cache = createViewCache(buildRegistry(fixtureGroups, true, { "./fixtures/widgets/newgroup/views.tsx": load }), { "fixture.counter": View });
    expect(cache.peek("fixture.counter")).toBe(View);
    await expect(cache.load("fixture.counter")).resolves.toBe(View);
    expect(load).not.toHaveBeenCalled();
  });

  it("КП4: a folder widgets/<new>/ (index.ts + views.tsx) appears in the registry without an edit to registry.ts", async () => {
    const r = buildRegistry({ ...legacyGroup, ...fixtureGroups }, true, fixtureViews);
    expect(r.descriptors.map((d) => d.id)).toEqual(["mentions", "triage", "sessions", "fixture.counter"]);
    // The view is not part of the registry: only a loader of the group's views.tsx, resolved on first use.
    expect(r.viewLoaders["fixture.counter"]).toBe(fixtureViews["./fixtures/widgets/newgroup/views.tsx"]);
    expect(typeof (await createViewCache(r).load("fixture.counter"))).toBe("function");
    // A group is catalog-only only when it says so: the fixture exports no `catalogOnly`.
    expect(r.catalogOnly.size).toBe(0);
    // Positive control: without the folder the id is not there.
    expect(buildRegistry(legacyGroup, true, fixtureViews).descriptors.map((d) => d.id)).not.toContain("fixture.counter");
  });

  it("no group's index.ts exports views: they belong in views.tsx, out of the registry chunk", () => {
    const realGroups = import.meta.glob("../widgets/*/index.ts", { eager: true }) as Groups;
    const eagerViews = (groups: Groups) => Object.keys(groups).filter((path) => "views" in groups[path]!);
    expect(Object.keys(realGroups)).toContain("../widgets/legacy/index.ts");
    expect(eagerViews(realGroups)).toEqual([]);
    // Positive control: the old shape (views next to the descriptors) is caught.
    expect(eagerViews(eagerGroups)).toEqual(["./fixtures/widgets/eagerviews/index.ts"]);
  });

  it("contract C: a group's index.ts holds descriptors only; its runtime imports are data files with type-only imports", () => {
    // A runtime import puts its module into the registry chunk, which SchemaForm screens load; a descriptor needs
    // data, not code. index.ts may import a data file (a settings schema, a size) only if that file itself has
    // nothing but `import type` and JSON: so no hook, view or React reaches the registry chunk through it. The core
    // catalog (core-catalog.ts over core-widgets.json, sizes and categories from catalog.go) is such a file.
    const runtimeImports = (src: string) =>
      [...src.matchAll(/^\s*(?:import|export)\s+(?!type\b)(?:[^;]*?\bfrom\s+)?["']([^"']+)["']/gm)].map((m) => m[1]!);
    const resolve = (from: string, spec: string) => {
      const parts = from.split("/").slice(0, -1);
      for (const seg of spec.split("/")) {
        if (seg === "..") parts.pop();
        else if (seg !== ".") parts.push(seg);
      }
      const p = parts.join("/");
      return [`${p}.ts`, `${p}/index.ts`];
    };
    const isJSON = (spec: string) => spec.endsWith(".json");
    const violations = (sources: Record<string, string>) =>
      Object.entries(sources)
        .filter(([path]) => path.endsWith("/index.ts"))
        .flatMap(([path, src]) =>
          runtimeImports(src)
            .filter((spec) => {
              if (isJSON(spec)) return false;
              const target = resolve(path, spec).find((p) => p in sources);
              return !target || runtimeImports(sources[target]!).some((s) => !isJSON(s));
            })
            .map((spec) => `${path} -> ${spec}`),
        );
    const real = import.meta.glob<string>(["../widgets/*/*.ts", "../core-catalog.ts"], { query: "?raw", import: "default", eager: true });
    expect(Object.keys(real)).toContain("../widgets/legacy/index.ts");
    expect(Object.keys(real)).toContain("../widgets/events/schema.ts");
    expect(Object.keys(real)).toContain("../core-catalog.ts");
    expect(violations(real)).toEqual([]);
    // Positive controls: a descriptor file that imports a hook module, and one whose data file imports a hook.
    const bad = import.meta.glob<string>("./fixtures/widgets/runtimeimport/*.ts", { query: "?raw", import: "default", eager: true });
    expect(violations(bad).sort()).toEqual([
      "./fixtures/widgets/runtimeimport/index.ts -> ../../../../hooks",
      "./fixtures/widgets/runtimeimport/index.ts -> ./schema",
    ]);
  });

  it("a failed views load is not remembered: the next load (the widget's retry) tries again", async () => {
    const View = () => null;
    const load = vi.fn<WidgetViewsLoader>().mockRejectedValueOnce(new Error("chunk")).mockResolvedValue({ views: { "fixture.counter": View } });
    const cache = createViewCache(buildRegistry(fixtureGroups, true, { "./fixtures/widgets/newgroup/views.tsx": load }));
    expect(cache.peek("fixture.counter")).toBeUndefined();
    await expect(cache.load("fixture.counter")).rejects.toThrow("chunk");
    expect(cache.peek("fixture.counter")).toBeUndefined();
    await expect(cache.load("fixture.counter")).resolves.toBe(View);
    expect(cache.peek("fixture.counter")).toBe(View);
    await cache.load("fixture.counter");
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("a duplicate id is an error in dev and skipped (first wins) in prod", () => {
    const modules = { ...legacyGroup, ...dupGroups };
    expect(() => buildRegistry(modules, true)).toThrow(/duplicate widget id "mentions"/);
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const r = buildRegistry(modules, false);
    expect(r.descriptors.filter((d) => d.id === "mentions").map((d) => d.title)).toEqual(["Mentions"]);
    expect(error).toHaveBeenCalled();
    error.mockRestore();
  });

  it("a module without descriptors is skipped with a warning, not fatal", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const r = buildRegistry({ "./widgets/broken/index.ts": {} }, true);
    expect(r.descriptors).toEqual([]);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});

describe("default layout of the legacy widgets and the v1 adapter", () => {
  it("two per row, 6 cells wide, rows as tall as the widget (§5.8: 6×4): the old two-column order", () => {
    const inst = defaultInstances(DEFAULTS, 2);
    expect(inst.map((i) => [i.id, i.x, i.y, i.w, i.h])).toEqual([
      ["mentions", 0, 0, 6, 4], ["triage", 6, 0, 6, 4], ["my-tasks", 0, 4, 6, 4], ["sessions", 6, 4, 6, 4],
    ]);
    expect(defaultInstances(DEFAULTS, 1).map((i) => [i.x, i.y, i.w])).toEqual([[0, 0, 12], [0, 4, 12], [0, 8, 12], [0, 12, 12]]);
  });

  it("defaultInstances: a row is as tall as its tallest widget, no overlap", () => {
    const tall = (id: string, h: number) =>
      ({ ...WIDGETS[0]!, id, size: { ...WIDGETS[0]!.size, min: { w: 3, h: 1 }, default: { w: 6, h } } }) as (typeof WIDGETS)[number];
    const inst = defaultInstances([tall("a", 1), tall("b", 3), tall("c", 1)], 2);
    expect(inst.map((i) => [i.id, i.x, i.y, i.h])).toEqual([["a", 0, 0, 1], ["b", 6, 0, 3], ["c", 0, 3, 1]]);
  });

  it("defaultInstances: sizes stay inside min/max and nothing overlaps, also for a full-width minimum", () => {
    const mk = (id: string, min: { w: number; h: number }, def: { w: number; h: number }, max: { w: number; h: number }) =>
      ({ ...LEGACY[0]!, id, size: { default: def, min, max } }) as (typeof LEGACY)[number];
    const ds = [
      mk("wide", { w: 12, h: 1 }, { w: 12, h: 2 }, { w: 12, h: 12 }),
      mk("a", { w: 1, h: 1 }, { w: 6, h: 2 }, { w: 12, h: 12 }),
      mk("b", { w: 4, h: 3 }, { w: 6, h: 3 }, { w: 12, h: 12 }),
      mk("c", { w: 1, h: 1 }, { w: 6, h: 1 }, { w: 3, h: 12 }),
    ];
    for (const cols of [1, 2, 3, 4]) {
      const inst = defaultInstances(ds, cols);
      inst.forEach((i, n) => {
        const s = ds[n]!.size;
        expect(i.w).toBeGreaterThanOrEqual(s.min.w);
        expect(i.w).toBeLessThanOrEqual(s.max.w);
        expect(i.h).toBeGreaterThanOrEqual(s.min.h);
        expect(i.x + i.w).toBeLessThanOrEqual(12);
      });
      for (let p = 0; p < inst.length; p++)
        for (let q = p + 1; q < inst.length; q++) {
          const a = inst[p]!, b = inst[q]!;
          const overlap = a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
          expect(overlap, `${cols} cols: ${a.id} vs ${b.id}`).toBe(false);
        }
    }
    expect(defaultInstances([ds[0]!], 2)[0]!.w).toBe(12);
  });

  const v2 = (): Omit<DashboardLayoutData, "components"> => ({
    columns: 12,
    widgets: DEFAULTS.map((descriptor, i) => ({ descriptor, instance: defaultInstances(DEFAULTS, 2)[i]! })).reverse(),
    available: [],
    editing: true,
    canEdit: true,
  });

  it("toLayoutV1: (y, x) order, v1 sizes, editing off", () => {
    const W = () => null;
    const v1 = toLayoutV1(v2(), W);
    expect(v1.widgets.map((w) => w.descriptor.id)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    // §5.8 mentions 6×4 (3×2–12×10), cut to the v1 grid's 3 rows.
    expect(v1.widgets.map((w) => w.size)).toEqual(Array(4).fill({ w: 2, h: 3 }));
    expect(v1.widgets[0]!.descriptor.size).toEqual({ default: { w: 2, h: 3 }, min: { w: 1, h: 2 }, max: { w: 4, h: 3 } });
    expect(v1.editing).toBe(false);
    expect(v1.components.Widget).toBe(W);
  });

  it("toLayoutV1 cuts a size the v1 grid cannot hold", () => {
    const d = v2();
    d.widgets[0]!.instance = { ...d.widgets[0]!.instance, w: 12, h: 9 };
    expect(toLayoutV1(d, () => null).widgets.find((w) => w.descriptor.id === "sessions")!.size).toEqual({ w: 4, h: 3 });
  });

  it("toActionsV1 passes openTask only", () => {
    const a = toActionsV1({ openTask: () => {}, moveWidget: async () => {}, removeWidget: async () => {} });
    expect(Object.keys(a)).toEqual(["openTask"]);
  });

  it("a v1 descriptor becomes a v2 one: columns x3, rows as they are", () => {
    const d = descriptorFromV1({ id: "t.x", title: "X", description: "", origin: "theme", dataHook: "useTasks", actions: [], size: { default: { w: 2, h: 1 }, min: { w: 1, h: 1 }, max: { w: 4, h: 2 } } });
    expect(d.size).toEqual({ default: { w: 6, h: 1 }, min: { w: 3, h: 1 }, max: { w: 12, h: 2 } });
    expect(d.dataHook).toBe("useTasks");
  });
});
