import { describe, expect, it } from "vitest";
import { colsForWidth, coreWidgetId, placeWidgets, sortWidgets, spanPx, type WidgetInstance } from "../model";
import { WIDGETS } from "../../registry";

// The acceptance set: 12×2, 6×4, 6×4, 3×2.
const W = (id: string, x: number, y: number, w: number, h: number): WidgetInstance => ({ id, widget_type: "mentions", x, y, w, h });
const SET = [W("a", 0, 0, 12, 2), W("b", 0, 2, 6, 4), W("c", 6, 2, 6, 4), W("d", 0, 6, 3, 2)];

describe("CT-dash v2 · coordinates", () => {
  it("breakpoints: 12 from 1280, 6 from 768, else 1", () => {
    expect([1440, 1280, 1279, 1024, 768, 767, 393].map(colsForWidth)).toEqual([12, 12, 6, 6, 6, 1, 1]);
  });
  it("12 columns keep x, y, w, h as 1-based grid lines", () => {
    const p = placeWidgets(SET, 12);
    expect(p.map((c) => [c.widget.id, c.colStart, c.colEnd, c.rowStart, c.rowSpan])).toEqual([
      ["a", 1, 13, 1, 2],
      ["b", 1, 7, 3, 4],
      ["c", 7, 13, 3, 4],
      ["d", 1, 4, 7, 2],
    ]);
  });
  it("6 columns scale proportionally and neighbours still touch without overlap", () => {
    const p = placeWidgets(SET, 6);
    expect(p.map((c) => [c.widget.id, c.colStart, c.colEnd])).toEqual([["a", 1, 7], ["b", 1, 4], ["c", 4, 7], ["d", 1, 3]]);
    const odd = placeWidgets([W("l", 0, 0, 3, 2), W("r", 3, 0, 3, 2)], 6);
    expect(odd[0]!.colEnd).toBe(odd[1]!.colStart);
  });
  it("one column: no column lines, order y then x, heights kept", () => {
    const shuffled = [SET[3]!, SET[2]!, SET[0]!, SET[1]!];
    const p = placeWidgets(shuffled, 1);
    expect(p.map((c) => c.widget.id)).toEqual(["a", "b", "c", "d"]);
    expect(p.every((c) => c.colStart === null && c.colEnd === null)).toBe(true);
    expect(p.map((c) => c.rowSpan)).toEqual([2, 4, 4, 2]);
  });
  it("no widget is lost, whatever the column count, even with broken numbers", () => {
    const bad = [...SET, W("e", 50, -3, 99, 0), { ...W("f", 0, 0, 1, 1), x: NaN }];
    for (const cols of [12, 6, 1] as const) {
      const p = placeWidgets(bad, cols);
      expect(p.map((c) => c.widget.id).sort()).toEqual(["a", "b", "c", "d", "e", "f"]);
      for (const c of p) {
        expect(c.rowSpan).toBeGreaterThanOrEqual(1);
        if (c.colStart !== null) expect(c.colEnd!).toBeGreaterThan(c.colStart);
        if (c.colEnd !== null) expect(c.colEnd).toBeLessThanOrEqual(cols + 1);
      }
    }
  });
  it("sort is stable on exact ties", () => {
    expect(sortWidgets([W("b", 0, 0, 1, 1), W("a", 0, 0, 1, 1)]).map((w) => w.id)).toEqual(["a", "b"]);
  });
  it("span height includes the gaps", () => {
    expect([spanPx(1), spanPx(2), spanPx(4)]).toEqual([72, 160, 336]);
  });
});

describe("CT-dash v2 · server widget types", () => {
  it("resolves agents-active to its own view, not the v1 sessions widget", () => {
    expect(coreWidgetId("agents-active")).toBe("agents-active");
    expect(WIDGETS.find((d) => d.id === coreWidgetId("agents-active"))?.title).toBe("Active agents");
  });
  it("resolves triage-queue to its own view with Move, not the v1 triage widget", () => {
    expect(coreWidgetId("triage-queue")).toBe("triage-queue");
    expect(WIDGETS.find((d) => d.id === coreWidgetId("triage-queue"))?.actions).toContain("moveTask");
  });
  it("a v1 'triage' instance still draws the v1 widget", () => {
    expect(coreWidgetId("triage")).toBe("triage");
  });
});
