import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { setLanguage } from "@/i18n";
import { Controls } from "../controls";
import { DEFAULT_STATE, type StatsState } from "../state";
import { usageFixture } from "./fixtures";

const st = (_key: string, en: string) => en;
beforeEach(async () => {
  await setLanguage("en");
});
afterEach(cleanup);

describe("statistics settings", () => {
  // The page keeps the state in the URL, which catches up a render later: two quick changes in a row must both
  // land, the second one must not rebuild from the props the first one has already left behind.
  it("composes two changes made before the page re-renders", () => {
    const sent: StatsState[] = [];
    const onChange = vi.fn((s: StatsState) => sent.push(s));
    const state: StatsState = { ...DEFAULT_STATE, m: "tokens" };
    render(<Controls state={state} onChange={onChange} caps={usageFixture().capabilities} st={st} />);
    fireEvent.change(screen.getAllByTestId("tokens-select")[0]!, { target: { value: "cache_read" } });
    fireEvent.change(screen.getAllByTestId("stack-select")[0]!, { target: { value: "agent" } });
    expect(sent[sent.length - 1]).toMatchObject({ m: "tokens", t: "cache_read", s: "agent" });
  });

  it("starts from the new props once the page has caught up", () => {
    const sent: StatsState[] = [];
    const onChange = (s: StatsState) => sent.push(s);
    const caps = usageFixture().capabilities;
    const { rerender } = render(<Controls state={{ ...DEFAULT_STATE, m: "tokens", t: "cache_read" }} onChange={onChange} caps={caps} st={st} />);
    rerender(<Controls state={{ ...DEFAULT_STATE, m: "tokens", t: "out", g: "agent" }} onChange={onChange} caps={caps} st={st} />);
    fireEvent.change(screen.getAllByTestId("stack-select")[0]!, { target: { value: "project" } });
    expect(sent[sent.length - 1]).toMatchObject({ t: "out", g: "agent", s: "project" });
  });

  // at 393 the strip scrolls sideways inside itself, and a chip in it sat past the screen's edge (x=824 of
  // 393): a single pool's slice read as the whole workspace. Active filters wrap in their own row under the strip.
  describe("narrow: active filters", () => {
    const caps = { ...usageFixture().capabilities, group_by: [...usageFixture().capabilities.group_by, "account" as const] };
    const narrow = () => screen.getByTestId("stats-chips");
    const strip = () => narrow().querySelector(".overflow-x-auto")!;

    it("puts the account chip in a wrapping row outside the sideways strip, with a working ×", () => {
      const sent: StatsState[] = [];
      const state: StatsState = { ...DEFAULT_STATE, m: "tokens", g: "agent", f: { account: ["claude/bob-team"] } };
      render(<Controls state={state} onChange={(s) => sent.push(s)} caps={caps} st={st} />);
      const row = screen.getByTestId("active-filters");
      expect(narrow().contains(row)).toBe(true);
      expect(strip().contains(row)).toBe(false);
      expect(row.className).toContain("flex-wrap");
      const chips = Array.from(narrow().querySelectorAll("[data-testid=filter-chip]"));
      expect(chips).toHaveLength(1);
      expect(row.contains(chips[0]!)).toBe(true);
      expect(chips[0]!.textContent).toContain("claude/bob-team");
      expect(chips[0]!.className).toContain("max-w-full");
      expect(strip().textContent).not.toContain("All projects");
      fireEvent.click(chips[0]!.querySelector("button")!);
      expect(sent[sent.length - 1]!.f.account ?? []).toEqual([]);
    });

    it("keeps every chip of several filters in the row", () => {
      const state: StatsState = { ...DEFAULT_STATE, m: "tokens", f: { account: ["claude/bob-team"], model: ["claude-opus-5-5"] } };
      render(<Controls state={state} onChange={() => {}} caps={caps} st={st} />);
      expect(screen.getByTestId("active-filters").querySelectorAll("[data-testid=filter-chip]")).toHaveLength(2);
      expect(strip().querySelectorAll("[data-testid=filter-chip]")).toHaveLength(0);
    });

    it("no filter: no extra row, the strip says «All projects»", () => {
      render(<Controls state={{ ...DEFAULT_STATE, m: "tokens" }} onChange={() => {}} caps={caps} st={st} />);
      expect(screen.queryByTestId("active-filters")).toBeNull();
      expect(strip().textContent).toContain("All projects");
    });

    it("limits view: no filter row (limits belong to an account, filters do not apply)", () => {
      render(<Controls state={{ ...DEFAULT_STATE, m: "quota", f: { account: ["claude/bob-team"] } }} onChange={() => {}} caps={{ ...caps, quota: true }} st={st} />);
      expect(screen.queryByTestId("active-filters")).toBeNull();
    });
  });
});
