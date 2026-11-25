import { describe, expect, it } from "vitest";
import { CORE_WIDGETS, buildRegistry } from "../registry";
import { CORE_TYPES, coreSize, coreTypeId, coreWidgetType } from "../core-catalog";
import type { WidgetDescriptor } from "../types";

const fmt = (s: WidgetDescriptor["size"]) =>
  `${s.default.w}×${s.default.h} (${s.min.w}×${s.min.h}–${s.max.w}×${s.max.h})`;

/** Every core descriptor against the server catalog's copy: one line per mismatch. */
function mismatches(descriptors: readonly WidgetDescriptor[]): string[] {
  const out: string[] = [];
  for (const d of descriptors.filter((x) => x.origin === "core")) {
    const t = coreWidgetType(d.id);
    if (!t) {
      out.push(`${d.id}: not a core type of catalog.go`);
      continue;
    }
    if (fmt(d.size) !== fmt(t.size)) out.push(`${d.id}: size web ${fmt(d.size)}, catalog ${fmt(t.size)}`);
    if (d.category !== t.category) out.push(`${d.id}: category web ${d.category}, catalog ${t.category}`);
  }
  return out;
}

describe("core widget sizes and categories = catalog.go (§5.8)", () => {
  it("every core descriptor of the registry has the catalog's size and category", () => {
    const rows = CORE_WIDGETS.map((d) => `${d.id.padEnd(16)} → ${coreTypeId(d.id).padEnd(16)} ${d.category.padEnd(14)} ${fmt(d.size)}`);
    console.info(`core widgets (web = catalog.go):\n${rows.join("\n")}`);
    expect(mismatches(CORE_WIDGETS)).toEqual([]);
  });

  it("every core id the client draws is in the catalog: the 22 types of §5.8 plus memory-usage (U6) and agent-usage (U4)", () => {
    expect(CORE_TYPES).toHaveLength(24);
    expect(coreWidgetType("memory-usage")?.category).toBe("analytics");
    expect(CORE_WIDGETS.some((d) => d.id === "agent-usage")).toBe(true);
    expect(CORE_WIDGETS.map((d) => d.id).filter((id) => !coreWidgetType(id))).toEqual([]);
    expect(coreTypeId("triage")).toBe("triage-queue");
    expect(coreTypeId("sessions")).toBe("agents-active");
  });

  it("positive control: a descriptor off the catalog is reported, by id", () => {
    const off = buildRegistry(
      {
        "./widgets/x/index.ts": {
          descriptors: [
            { ...CORE_WIDGETS.find((d) => d.id === "initiatives")!, size: { ...coreSize("initiatives"), max: { w: 12, h: 6 } } },
            { ...CORE_WIDGETS.find((d) => d.id === "event-feed")!, category: "activity" },
            { ...CORE_WIDGETS.find((d) => d.id === "event-feed")!, id: "no-such-core-widget" },
          ],
        },
      },
      true,
    ).descriptors;
    expect(mismatches(off)).toEqual([
      "initiatives: size web 12×4 (4×3–12×6), catalog 12×4 (4×3–12×10)",
      "event-feed: category web activity, catalog events",
      "no-such-core-widget: not a core type of catalog.go",
    ]);
  });

  it("coreSize hands every descriptor its own copy", () => {
    const a = coreSize("mentions");
    a.max.h = 1;
    expect(coreSize("mentions").max.h).toBe(10);
  });
});
