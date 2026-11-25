import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { Controls } from "../controls";
import { UsagePanel } from "../usage-panel";
import { QuotaPanel } from "../quota-panel";
import { sanitize } from "../stats-body";
import { DEFAULT_STATE } from "../state";
import type { ST } from "../format";
import { quotaFixture, usageFixture } from "./fixtures";

const st: ST = (_key, text, opts) => text.replace(/\{\{(\w+)\}\}/g, (_, k) => String(opts?.[k] ?? k));
afterEach(cleanup);

describe("capability-driven controls", () => {
  it("hides pools, pool filters and limits for a member; admin is the positive control", () => {
    const admin = usageFixture().capabilities;
    const member = { ...admin, quota: false, group_by: admin.group_by.filter((d) => d !== "account") };
    const view = render(<Controls state={DEFAULT_STATE} onChange={vi.fn()} caps={member} st={st} />);
    expect(screen.queryByRole("radio", { name: "Subscription limits" })).toBeNull();
    expect(screen.queryAllByRole("option").filter((o) => o.getAttribute("value") === "account")).toHaveLength(0);
    expect(sanitize({ ...DEFAULT_STATE, m: "quota", g: "account", s: "account", f: { account: ["private"], agent: ["a"] } }, member)).toMatchObject({ m: "cost", g: "model", s: null, f: { agent: ["a"] } });
    view.rerender(<Controls state={DEFAULT_STATE} onChange={vi.fn()} caps={admin} st={st} />);
    expect(screen.getByRole("radio", { name: "Subscription limits" })).toBeTruthy();
    expect(screen.getAllByRole("option").some((o) => o.getAttribute("value") === "account")).toBe(true);
  });

  it("does not expose limits before capabilities arrive, even for a pasted quota URL", () => {
    render(<Controls state={{ ...DEFAULT_STATE, m: "quota" }} onChange={vi.fn()} caps={undefined} st={st} />);
    expect(screen.queryByRole("radio", { name: "Subscription limits" })).toBeNull();
    expect(sanitize(DEFAULT_STATE, { group_by: ["agent"], cost: false, quota: false })).toMatchObject({ m: "tokens", g: "agent" });
  });
});

describe("usage and limits results", () => {
  it("keeps unknown rows, totals and coverage; column tooltip explains model prices", () => {
    const change = vi.fn();
    render(<UsagePanel data={usageFixture()} state={DEFAULT_STATE} onChange={change} mode="page" links={{}} st={st} />);
    expect(screen.getByTestId("usage-total-row").textContent).toContain("$30.00");
    expect(screen.getByTestId("coverage-strip").textContent).toContain("50%");
    const row = document.querySelector('tr[data-row="unknown-model"]')!;
    expect(row.textContent).toContain("none");
    fireEvent.mouseEnter(document.querySelector("button[data-bucket]")!);
    expect(screen.getByRole("tooltip").textContent).toMatch(/API price as of.*2026.*opus/);
    expect(screen.getByRole("tooltip").textContent).toContain("unknown-model: no price");
    fireEvent.click(within(document.querySelector('tr[data-row="opus"]') as HTMLElement).getByRole("button", { name: "Only this" }));
    expect(change.mock.calls[0]![0].f).toEqual({ model: ["opus"] });
  });

  // The hint under the chart says «click a table row»: any cell keeps the row, the row's own buttons keep theirs.
  it("keeps a row on a click anywhere in it, not only on its name", () => {
    const change = vi.fn();
    render(<UsagePanel data={usageFixture()} state={DEFAULT_STATE} onChange={change} mode="page" links={{}} st={st} />);
    const row = document.querySelector('tr[data-row="opus"]') as HTMLElement;
    fireEvent.click(row.querySelectorAll("td")[3]!);
    expect(change).toHaveBeenCalledTimes(1);
    expect(change.mock.calls[0]![0].f).toEqual({ model: ["opus"] });
  });

  it("labels only an API-key slice as actual spend", () => {
    render(<UsagePanel data={usageFixture()} state={{ ...DEFAULT_STATE, f: { billing_mode: ["api"] } }} onChange={vi.fn()} mode="page" links={{}} st={st} />);
    fireEvent.mouseEnter(document.querySelector("button[data-bucket]")!);
    expect(screen.getByRole("tooltip").textContent).toContain("Actual spend of API keys");
  });

  it("shows an empty period without inventing a zero price", () => {
    const data = usageFixture();
    data.rows = []; data.totals = { value: null, turns: 0, turns_unknown: 0 };
    render(<UsagePanel data={data} state={DEFAULT_STATE} onChange={vi.fn()} mode="page" links={{}} st={st} />);
    expect(screen.getByTestId("stats-empty").textContent).toContain("No agent turns");
  });

  it("shows missing quota reasons and contributors for the same account and period", () => {
    const change = vi.fn();
    render(<QuotaPanel data={quotaFixture()} state={{ ...DEFAULT_STATE, m: "quota", p: "30d" }} onChange={change} mode="page" st={st} />);
    // kimi/alex has no weekly window: it is a «no data» row, not one of M (Doc §3.2 v4).
    expect(screen.getByTestId("quota-near").textContent).toBe("1 of 1");
    expect(screen.getAllByText("No weekly window; monthly 33%").length).toBeGreaterThan(0);
    expect(screen.queryByTestId("usage-total-row")).toBeNull();
    fireEvent.click(within(document.querySelector('tr[data-row="claude/bob-team"]') as HTMLElement).getByRole("button", { name: "Who spent it" }));
    expect(change.mock.calls[0]![0]).toMatchObject({ m: "tokens", g: "agent", p: "30d", f: { account: ["claude/bob-team"] } });
  });

  // The deployed quota API (U3) sends the reason as {code, text, other_windows}, not a string;
  // rendering it as a React child crashed the page (prod, 2026-11-19). The page builds the text from the code in its
  // own language; the Russian strings are in mockup-polish.test.tsx.
  it("renders the API's structured no-data reason from its code", () => {
    const data = quotaFixture();
    data.rows[1] = {
      account: "kimi/alex", now: null, status: "unknown",
      reason: {
        code: "no_window", text: "no week window: 5h — 71%, month — 33%",
        other_windows: [{ window: "5h", window_raw: "5h", now: 0.71, status: "available" }],
      },
    };
    render(<QuotaPanel data={data} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={() => {}} mode="page" st={st} />);
    expect(screen.getAllByText("no «week» window: 5 h — 71%").length).toBeGreaterThan(0);
    expect(document.querySelector('tr[data-row="kimi/alex"]')!.textContent).toContain("no «week» window");
  });

  // Prod 19.11 (Doc §3.2 v4): an exhausted account with its fraction is AT the limit, not «no data»;
  // M counts the accounts that have the chosen window. Prod-shaped names, as the API sends them after the fix.
  it("counts an exhausted account at the limit and leaves accounts without the window out of M", () => {
    const data = quotaFixture();
    data.series = [
      { key: "claude/alex", label: "claude/alex", values: [null, null] },
      { key: "claude/bob-team", label: "claude/bob-team", values: [null, 0.75] },
    ];
    data.rows = [
      { account: "claude/bob-team", window_raw: "7d", now: 1, peak: 1, hours_at_limit: 4, status: "exhausted", reset_at: "2026-11-20T00:00:00Z" },
      { account: "glm/alex", window_raw: "1w", now: 0.19, peak: 0.92, hours_at_limit: 1, status: "available" },
      { account: "claude/alex", window_raw: "7d", now: null, status: "unknown", reason: { code: "no_data", text: "source provided no data (unknown)" } },
      { account: "kimi/alex", window_raw: null, now: null, status: null as unknown as string, reason: { code: "no_window", text: "no week window: 5h — 50%, month — 9%" } },
    ];
    const change = vi.fn();
    render(<QuotaPanel data={data} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={change} mode="page" st={st} />);
    expect(screen.getByTestId("quota-near").textContent).toBe("1 of 3");
    const bob = document.querySelector('tr[data-row="claude/bob-team"]') as HTMLElement;
    expect(bob.getAttribute("data-nodata")).toBeNull();
    expect(bob.querySelector("td:nth-child(2)")!.textContent).toBe("100%");
    expect(bob.querySelector("td:nth-child(2)")!.className).toContain("text-destructive");
    expect(screen.getByTestId("quota-nodata-count").textContent).toContain("2 pools have no");
    fireEvent.click(within(bob).getByRole("button", { name: "Who spent it" }));
    expect(change.mock.calls[0]![0]).toMatchObject({ m: "tokens", g: "agent", f: { account: ["claude/bob-team"] } });
    // The first palette colour goes to the drawn line, not to the empty claude/alex series listed before it.
    const swatch = bob.querySelector("td:first-child [style]") as HTMLElement;
    expect(swatch.getAttribute("style")).toContain("--chart-1");
  });

  // An account at its limit reports status=exhausted with its share (prod 19.11: claude/bob-team 7d at 1.0); it is
  // «at the limit», not «no data». M counts only the accounts that have the chosen window.
  it("counts exhausted with a share at the limit and leaves accounts without the window out of M", () => {
    const data = quotaFixture();
    data.rows = [
      { account: "claude/bob-team", now: 1, peak: 1, hours_at_limit: 3, status: "exhausted", reset_at: "2026-11-20T00:00:00Z" },
      { account: "codex/alex", now: 0.4, status: "available" },
      { account: "claude/dead", now: null, status: "exhausted", reason: { code: "no_data", text: "source provided no data (exhausted)" } },
      { account: "kimi/alex", now: null, status: "", reason: { code: "no_window", text: "no week window: month — 33%" } },
    ];
    render(<QuotaPanel data={data} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={() => {}} mode="page" st={st} />);
    expect(screen.getByTestId("quota-near").textContent).toBe("1 of 3");
    expect(document.querySelector('tr[data-row="claude/bob-team"]')!.getAttribute("data-nodata")).toBeNull();
    expect(document.querySelector('tr[data-row="claude/bob-team"] td:nth-child(2)')!.textContent).toBe("100%");
    expect(document.querySelector('tr[data-row="claude/dead"]')!.getAttribute("data-nodata")).toBe("true");
    expect(document.querySelector('tr[data-row="kimi/alex"]')!.getAttribute("data-nodata")).toBe("true");
  });

  // One threshold for every limit signal: 89% is under it, 90% is at it — the count, the red
  // share and the at-limit column all read the same 90%.
  it("counts a pool at the limit from 90%, not from 89%", () => {
    const data = quotaFixture();
    data.rows = [
      { account: "claude/team-a", now: 0.89, peak: 0.89, hours_at_limit: 0, status: "available" },
      { account: "claude/team-b", now: 0.9, peak: 0.9, hours_at_limit: 1, status: "available" },
    ];
    render(<QuotaPanel data={data} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={() => {}} mode="page" st={st} />);
    expect(screen.getByTestId("quota-near").textContent).toBe("1 of 2");
    const share = (account: string) => document.querySelector(`tr[data-row="${account}"] td:nth-child(2)`)!;
    expect(share("claude/team-a").textContent).toBe("89%");
    expect(share("claude/team-a").className).not.toContain("text-destructive");
    expect(share("claude/team-b").className).toContain("text-destructive");
    expect(screen.getByRole("columnheader", { name: "At limit (≥ 90%)" })).toBeTruthy();
    expect(screen.getByText(/Dashed: the 90% threshold/)).toBeTruthy();
  });
});


describe("stacked project labels", () => {
  const projectId = "c6e35032-36d5-4045-b30d-6cf9e35c3dee";
  const projectName = "Mesh — статистика и управление проектами с длинным названием";

  it.each(["page", "widget"] as const)("shows project names in both %s layouts and keeps account filter keys", (mode) => {
    const data = usageFixture();
    data.query.group_by = "account";
    data.query.stack_by = "project";
    data.rows = [Object.assign({ ...data.rows[0]!, key: "codex/alex", label: "codex/alex", stack_key: projectId }, { stack_label: projectName })];
    const change = vi.fn();
    render(<UsagePanel data={data} state={{ ...DEFAULT_STATE, g: "account", s: "project" }} onChange={change} mode={mode} links={{}} st={st} />);
    const table = screen.getByTestId("usage-table");
    expect(within(table).getAllByText(projectName)).toHaveLength(2);
    expect(within(table).queryByText(projectId)).toBeNull();
    const desktop = table.querySelector('tr[data-row="codex/alex"]') as HTMLElement;
    expect(desktop.textContent).toContain(projectName);
    expect(screen.getByTestId("usage-card").textContent).toContain(projectName);
    fireEvent.click(desktop.querySelector("td")!);
    expect(change.mock.calls[0]![0].f).toEqual({ account: ["codex/alex"] });
  });

  it.each([undefined, null, "", "   "])("falls back to the stack key for an unavailable name (%s)", (stack_label) => {
    const data = usageFixture();
    data.query.stack_by = "project";
    data.rows = [Object.assign({ ...data.rows[0]!, stack_key: projectId }, { stack_label })];
    render(<UsagePanel data={data} state={DEFAULT_STATE} onChange={vi.fn()} mode="widget" links={{}} st={st} />);
    expect(within(screen.getByTestId("usage-table")).getAllByText(projectId)).toHaveLength(2);
  });

  it.each(["", null, undefined])("shows the server's label when the stack key is unavailable (%s)", (stack_key) => {
    const data = usageFixture();
    data.query.stack_by = "project";
    data.rows = [{ ...data.rows[0]!, stack_key, stack_label: "Unspecified" }];
    render(<UsagePanel data={data} state={DEFAULT_STATE} onChange={vi.fn()} mode="widget" links={{}} st={st} />);
    expect(within(screen.getByTestId("usage-table")).getAllByText("Unspecified")).toHaveLength(2);
  });

  it.each(["", null, undefined])("shows a dash in both stacked layouts when label and key are unavailable (%s)", (stack_key) => {
    const data = usageFixture();
    data.query.stack_by = "project";
    data.rows = [{ ...data.rows[0]!, stack_key, stack_label: null }];
    render(<UsagePanel data={data} state={DEFAULT_STATE} onChange={vi.fn()} mode="widget" links={{}} st={st} />);
    const table = screen.getByTestId("usage-table");
    const desktop = table.querySelector("tbody tr td:first-child")!;
    expect(within(desktop as HTMLElement).getByText("—")).toBeInTheDocument();
    expect(within(screen.getByTestId("usage-card")).getAllByText("—").some((el) => el.tagName === "P")).toBe(true);
  });

  it("omits the project subtitle when no stack dimension was selected", () => {
    const data = usageFixture();
    data.query.stack_by = null;
    data.rows = [{ ...data.rows[0]!, stack_key: null, stack_label: "Unspecified" }];
    render(<UsagePanel data={data} state={DEFAULT_STATE} onChange={vi.fn()} mode="widget" links={{}} st={st} />);
    expect(within(screen.getByTestId("usage-table")).queryByText("Unspecified")).toBeNull();
  });
});
