// the page against the approved mockups, in Russian, on prod-shaped answers: the server's «Others»
// row is `__other__` / "Other" with no count, reasons come as {code, text, other_windows}, the limits answer starts a
// rolling period mid-day. Copy is read through the real i18n (ru), so a missing Russian key fails here.
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ComponentProps } from "react";
import { setLanguage } from "@/i18n";
import { useST } from "../format";
import { UsagePanel } from "../usage-panel";
import { QuotaPanel, trimToPeriod } from "../quota-panel";
import { DEFAULT_STATE, MAX_TOP } from "../state";
import { prodQuotaFixture as prodQuota, prodUsageFixture } from "./fixtures";

beforeEach(async () => {
  await setLanguage("ru");
});
afterEach(cleanup);
afterAll(async () => {
  await setLanguage("en");
});

/** Non-breaking and narrow spaces of ru number formats read as plain spaces. */
const text = (el: Element | null) => (el?.textContent ?? "").replace(/[\s  ]+/g, " ").trim();

function RuUsage(props: Omit<ComponentProps<typeof UsagePanel>, "st">) {
  return <UsagePanel {...props} st={useST()} />;
}
function RuQuota(props: Omit<ComponentProps<typeof QuotaPanel>, "st">) {
  return <QuotaPanel {...props} st={useST()} />;
}

const state = { ...DEFAULT_STATE, n: 5 };

describe("«≈ $» table against the mockup", () => {
  it("names the sort, counts «Others» and expands them; the prod «__other__» row is «Остальные (K)»", () => {
    const change = vi.fn();
    render(<RuUsage data={prodUsageFixture()} state={state} onChange={change} mode="page" links={{}} othersCount={{ all: 8, more: false }} updatedAt={Date.parse("2026-11-19T13:40:00Z")} />);
    expect(text(screen.getByTestId("usage-sort-caption"))).toBe("Сортировка: стоимость. Показаны 5 из 8, «Остальные» ниже");
    const others = document.querySelector('tr[data-row="__other__"]') as HTMLElement;
    expect(text(others.querySelector("td"))).toBe("Остальные (3)");
    fireEvent.click(within(others).getByRole("button", { name: "Раскрыть" }));
    expect(change.mock.calls[0]![0]).toMatchObject({ n: MAX_TOP });
    expect(text(screen.getByTestId("usage-updated"))).toBe("Обновлено 16:40 МСК");
  });

  // Rows without a sum keep the server's order among themselves (a stable sort).
  it("sorts by the value by default, rows without a sum last, «Остальные» at the bottom", () => {
    render(<RuUsage data={prodUsageFixture()} state={state} onChange={vi.fn()} mode="page" links={{}} />);
    const order = [...document.querySelectorAll("tbody tr")].map((tr) => tr.getAttribute("data-row"));
    expect(order).toEqual(["claude-opus-5-5", "claude-sonnet-5-5", "gpt-6.1-sol", "zcode", "kimi", "__other__"]);
  });

  it("gives «Остальные» the neutral colour, so five models and the rest repeat no palette slot", () => {
    render(<RuUsage data={prodUsageFixture()} state={state} onChange={vi.fn()} mode="page" links={{}} />);
    const swatch = (key: string) => (document.querySelector(`tr[data-row="${key}"] td:first-child [style]`) as HTMLElement).getAttribute("style")!;
    const named = ["claude-opus-5-5", "zcode", "kimi", "claude-sonnet-5-5", "gpt-6.1-sol"].map(swatch);
    expect(new Set(named).size).toBe(5);
    expect(swatch("__other__")).toContain("--status-backlog");
  });

  it("says «20+» when even the full list has more than the API's top", () => {
    render(<RuUsage data={prodUsageFixture()} state={state} onChange={vi.fn()} mode="page" links={{}} othersCount={{ all: 20, more: true }} />);
    expect(text(screen.getByTestId("usage-sort-caption"))).toBe("Сортировка: стоимость. Показаны 5 из 20+, «Остальные» ниже");
    expect(text(document.querySelector('tr[data-row="__other__"] td'))).toBe("Остальные (15+)");
  });

  it("translates «Other» in the agents table too", () => {
    const d = prodUsageFixture();
    d.query.group_by = "agent";
    d.query.metric = "tokens";
    render(<RuUsage data={d} state={{ ...state, m: "tokens", g: "agent" }} onChange={vi.fn()} mode="page" links={{}} />);
    expect(text(document.querySelector('tr[data-row="__other__"] td'))).toBe("Остальные");
    expect(document.body.textContent).not.toMatch(/\bOther\b/);
  });

  it("393: «по стоимости» over the cards and «Таблица целиком» in the bottom bar", () => {
    const change = vi.fn();
    render(<RuUsage data={prodUsageFixture()} state={state} onChange={change} mode="page" links={{}} othersCount={{ all: 8, more: false }} />);
    expect(text(screen.getByTestId("usage-sort-short"))).toBe("по стоимости");
    const bar = screen.getByTestId("sticky-total");
    expect(text(bar)).toContain("Итого за 7 дней");
    fireEvent.click(within(bar).getByRole("button", { name: "Таблица целиком" }));
    expect(change.mock.calls[0]![0]).toMatchObject({ n: MAX_TOP });
    expect(screen.getAllByTestId("usage-card").map((c) => text(c)).some((t) => t.startsWith("Остальные (3)"))).toBe(true);
  });
});

describe("«Лимиты подписок» against the mockup", () => {
  it("builds the Russian no-data reason from reason.code; an unknown code keeps the server text", () => {
    render(<RuQuota data={prodQuota()} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={vi.fn()} mode="page" />);
    const reason = (account: string) => text(document.querySelector(`tr[data-row="${account}"] td:nth-child(3)`));
    expect(reason("kimi/alex")).toBe("нет окна «неделя»: 5 ч — 9 %, месяц — 13 %");
    expect(reason("anthropic-api/bob")).toBe("нет окна «неделя»: бюджет — 18 %");
    expect(reason("claude/alex")).toBe("источник лимита не отдал данные");
    expect(reason("glm/odd")).toBe("observation is 3 days old");
    expect(reason("codex/none")).toBe("нет окна «неделя»");
  });

  it("captions the line by the chart's step: a day is not «на конец часа»", () => {
    const { rerender } = render(<RuQuota data={prodQuota()} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={vi.fn()} mode="page" />);
    const legend = () => text(screen.getByTestId("quota-legend"));
    expect(legend()).toContain("за день");
    expect(legend()).not.toContain("на конец часа");
    const hourly = prodQuota();
    hourly.query.step = "hour";
    rerender(<RuQuota data={hourly} state={{ ...DEFAULT_STATE, m: "quota", p: "1d" }} onChange={vi.fn()} mode="page" />);
    expect(legend()).toContain("за час");
  });

  it("starts the axis at the period's first day, not the day before the rolling start", () => {
    const trimmed = trimToPeriod(prodQuota());
    expect(trimmed.buckets[0]).toBe("2026-11-14T00:00:00+03:00");
    expect(trimmed.buckets).toHaveLength(7);
    expect(trimmed.series[0]!.values).toEqual([0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.09]);
    // A period that starts at midnight keeps every bucket.
    const usageLike = prodQuota();
    usageLike.query.from = "2026-11-13T00:00:00+03:00";
    expect(trimToPeriod(usageLike).buckets).toHaveLength(8);

    const width = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(900);
    try {
      render(<RuQuota data={prodQuota()} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={vi.fn()} mode="page" />);
      const labels = [...document.querySelectorAll('[data-testid="quota-chart"] svg text')].map((t) => t.textContent);
      const firstDay = labels.find((l) => /^\d{1,2}( |$)/.test(l ?? ""));
      expect(firstDay).toBe("14 сб");
      expect(labels.some((l) => /^13( |$)/.test(l ?? ""))).toBe(false);
    } finally {
      width.mockRestore();
    }
  });

  // Prod 20.11: a pool with a history in the period but no current share. The line is drawn, so its row
  // (and its card at 393) carries the line's colour, peak and at-limit hours; «Сейчас» is «нет данных» with the reason.
  it("gives a pool with history but no current share the line's colour, peak and hours; a pool with neither stays hatched", () => {
    const data = prodQuota();
    data.series = [
      ...data.series,
      { key: "claude/alex", label: "claude/alex", values: [null, null, null, null, null, null, null, null] },
      { key: "claude/riker-team", label: "claude/riker-team", values: [null, null, null, null, null, null, 0.99, 0.99] },
    ];
    data.rows = [...data.rows, { account: "claude/riker-team", now: null, peak: 0.99, hours_at_limit: 4, status: "unknown", reason: { code: "no_data", text: "source provided no data (unknown)" } }];
    const width = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(900);
    try {
      render(<RuQuota data={data} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={vi.fn()} mode="page" />);
    } finally {
      width.mockRestore();
    }
    const stroke = document.querySelector('[data-testid="quota-chart"] g[data-pool="claude/riker-team"] path')!.getAttribute("stroke");
    expect(stroke).toBeTruthy();

    const row = document.querySelector('tr[data-row="claude/riker-team"]') as HTMLElement;
    expect(row.getAttribute("data-nodata")).toBeNull();
    const cell = (n: number) => text(row.querySelector(`td:nth-child(${n})`));
    expect((row.querySelector("td:first-child [style]") as HTMLElement).style.backgroundColor).toBe(stroke);
    expect(cell(2)).toBe("нет данных");
    expect(cell(3)).toBe("источник лимита не отдал данные");
    expect(cell(5)).toBe("99 %");
    expect(cell(6)).toBe("4 ч");

    const card = screen.getAllByTestId("quota-card").find((li) => li.textContent!.includes("claude/riker-team"))!;
    expect((card.querySelector("[style]") as HTMLElement).style.backgroundColor).toBe(stroke);
    expect(text(card)).toContain("нет данных");
    expect(text(card)).toContain("источник лимита не отдал данные");
    const stats = [...card.querySelectorAll('[data-history="true"] > div')].map((d) => [...d.querySelectorAll("p")].map(text));
    expect(stats).toContainEqual(["Пик", "99 %"]);
    expect(stats).toContainEqual(["У лимита", "4 ч"]);

    // Every drawn line has a row of its colour; the KPI still reads only `now`: riker-team at 99 % in the past is not «at the limit».
    for (const g of document.querySelectorAll('[data-testid="quota-chart"] g[data-pool]')) {
      const key = g.getAttribute("data-pool")!;
      const values = data.series.find((s) => s.key === key)!.values.slice(1);
      if (!values.some((v) => v != null)) continue;
      const swatch = document.querySelector(`tr[data-row="${key}"] td:first-child [style]`) as HTMLElement;
      expect(swatch.style.backgroundColor).toBe(g.querySelector("path")!.getAttribute("stroke"));
    }
    expect(text(screen.getByTestId("quota-near"))).toMatch(/^0 /);

    // No values in the series and no peak: hatched as before, peak and hours «—».
    const dead = document.querySelector('tr[data-row="claude/alex"]') as HTMLElement;
    expect(dead.getAttribute("data-nodata")).toBe("true");
    expect(dead.querySelector("td:first-child [style]")).toBeNull();
    expect(text(dead.querySelector("td:nth-child(5)"))).toBe("—");
    expect(text(dead.querySelector("td:nth-child(6)"))).toBe("—");
  });

  // (live check 21.11 against 09e4e5af-v2-limits-1440/393): per day the wide axis reads «14 сб … 20 пт» as
  // the «≈ $» columns do, 393 keeps «14 15 …»; per hour it stays «14:00». «Now» is a dotted vertical line at the last point.
  const axisOf = () =>
    [...document.querySelectorAll('[data-testid="quota-chart"] svg > text')].map((t) => t.textContent ?? "").filter((l) => !/%|сейчас/.test(l));
  function renderQuota(data: ReturnType<typeof prodQuota>, px: number) {
    const width = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(px);
    try {
      render(<RuQuota data={data} state={{ ...DEFAULT_STATE, m: "quota" }} onChange={vi.fn()} mode="page" />);
    } finally {
      width.mockRestore();
    }
  }

  it.each([900, 1100])("labels each day with its weekday on the wide 7d axis (%ipx)", (px) => {
    renderQuota(prodQuota(), px);
    expect(axisOf()).toEqual(["14 сб", "15 вс", "16 пн", "17 вт", "18 ср", "19 чт", "20 пт"]);
  });

  // 393: the chart is about 329 px wide; the axis stays numbers only, as the 393 mockup and prod read.
  it.each([329, 361])("keeps the narrow 7d axis to day numbers (%ipx)", (px) => {
    renderQuota(prodQuota(), px);
    expect(axisOf()).toEqual(["14", "15", "16", "17", "18", "19", "20"]);
  });

  it("keeps hours without a weekday when the step is an hour", () => {
    const data = prodQuota();
    data.query = { ...data.query, from: "2026-11-20T00:00:00+03:00", to: "2026-11-21T00:00:00+03:00", step: "hour" };
    data.buckets = Array.from({ length: 24 }, (_, h) => `2026-11-20T${String(h).padStart(2, "0")}:00:00+03:00`);
    data.series = [{ key: "claude/bob-team", label: "claude/bob-team", values: data.buckets.map((_, h) => h / 30) }];
    renderQuota(data, 900);
    const axis = axisOf();
    expect(axis[0]).toBe("00:00");
    expect(axis.length).toBeGreaterThan(3);
    for (const l of axis) expect(l).toMatch(/^\d{2}:\d{2}$/);
  });

  // «При шаге час — как сейчас»: hours over several days keep main's day ticks at each day's first hour, unchanged here.
  it("keeps main's day ticks for hours over several days", () => {
    const data = prodQuota();
    data.query = { ...data.query, from: "2026-11-18T00:00:00+03:00", to: "2026-11-21T00:00:00+03:00", step: "hour" };
    data.buckets = Array.from({ length: 72 }, (_, h) => new Date(Date.parse("2026-11-18T00:00:00+03:00") + h * 3_600_000).toISOString());
    data.series = [{ key: "claude/bob-team", label: "claude/bob-team", values: data.buckets.map(() => 0.5) }];
    renderQuota(data, 900);
    expect(axisOf()).toEqual(["18 ср", "19 чт", "20 пт"]);
  });

  it("draws «сейчас» as a dotted vertical line at the last point, its caption beside it", () => {
    renderQuota(prodQuota(), 900);
    const now = document.querySelector('[data-testid="quota-chart"] [data-testid="quota-now"]')!;
    expect(now).not.toBeNull();
    const line = now.querySelector("line")!;
    const last = document.querySelector('[data-testid="quota-chart"] g[data-pool="claude/bob-team"] circle')!;
    expect(line.getAttribute("x1")).toBe(last.getAttribute("cx"));
    expect(line.getAttribute("x2")).toBe(last.getAttribute("cx"));
    expect(Number(line.getAttribute("y2")) - Number(line.getAttribute("y1"))).toBeGreaterThan(100);
    expect(line.getAttribute("stroke-dasharray")).toBeTruthy();
    expect(text(now.querySelector("text"))).toBe("сейчас");
  });
});
