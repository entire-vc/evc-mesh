import { afterEach, describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { CostQualityBlock, formatCost } from "@/components/cost-quality-block";
import i18n from "@/i18n";
import type { TaskCostSummary } from "@/lib/api";

const BASE: TaskCostSummary = {
  total_cost: 1.5,
  tokens_in: 2000,
  tokens_out: 500,
  session_count: 2,
  rework_count: 0,
  quality_flag: "multi-turn",
};

// An incomplete total is marked, a null total is a dash (never $0.00),
// and a plain report with a real zero stays distinct from unknown usage.
describe("CostQualityBlock unknown usage", () => {
  it("shows no mark for a complete total", () => {
    render(<CostQualityBlock summary={BASE} />);
    expect(screen.queryByTestId("incomplete-mark")).toBeNull();
    expect(screen.getByTestId("cost-total").textContent).toBe("≈$1.50");
  });

  it("keeps a real zero distinct from unknown usage without an incomplete mark", () => {
    render(<CostQualityBlock summary={{ ...BASE, total_cost: 0, tokens_in: 0, tokens_out: 0 }} />);
    expect(screen.queryByTestId("incomplete-mark")).toBeNull();
    expect(screen.getByTestId("cost-total").textContent).toBe("≈$0.00");
    expect(screen.getByTestId("cost-tokens").textContent).toContain("0 / 0");
  });

  it("marks only the measure that is incomplete", () => {
    render(<CostQualityBlock summary={{ ...BASE, tokens_in_incomplete: true }} />);
    const marks = screen.getAllByTestId("incomplete-mark");
    expect(marks).toHaveLength(1);
    expect(screen.getByTestId("cost-tokens").textContent).toMatch(/2\.0k\+ \/ 500/);
    expect(screen.getByTestId("cost-total").textContent).toBe("≈$1.50");
  });

  it("shows a dash with the mark for a total with no known part, not zero", () => {
    render(
      <CostQualityBlock
        summary={{
          ...BASE,
          total_cost: null,
          tokens_in: null,
          tokens_out: null,
          cost_incomplete: true,
          tokens_in_incomplete: true,
          tokens_out_incomplete: true,
        }}
      />,
    );
    expect(screen.getByTestId("cost-total").textContent).toMatch(/^—\+/);
    expect(screen.getByTestId("cost-total").textContent).not.toContain("$0");
    expect(screen.getAllByTestId("incomplete-mark")).toHaveLength(3);
  });
});

// The dollars are the CLI's API list price (costBasis "list"), not a charge: the fleet runs on
// subscriptions. The figure says "≈" and carries a caption with the full explanation in its title.
describe("CostQualityBlock API list price", () => {
  afterEach(async () => { await i18n.changeLanguage("en"); });

  it.each([
    ["en", "at API list price", "Notional cost at the API price list. A subscription spends window limits, not money"],
    ["ru", "по API-цене", "Условная стоимость по прайсу API. Подписка списывает лимиты окон, а не деньги"],
  ])("explains the API-price equivalent in %s", async (language, label, hint) => {
    await i18n.changeLanguage(language);
    render(<CostQualityBlock summary={{ ...BASE, total_cost: 6.4 }} />);
    expect(screen.getByTestId("cost-total").textContent).toBe("≈$6.40");
    const note = screen.getByTestId("cost-api-price");
    expect(note.textContent).toBe(label);
    expect(note.getAttribute("title")).toBe(hint);
  });

  it("labels a sub-cent cost as an API-price equivalent", () => {
    render(<CostQualityBlock summary={{ ...BASE, total_cost: 0.004 }} />);
    expect(screen.getByTestId("cost-total").textContent).toBe("≈<$0.01");
    expect(screen.getByTestId("cost-api-price").textContent).toBe("at API list price");
  });
  it("shows ≈$6.40 with the API-price caption and its explanation", () => {
    render(<CostQualityBlock summary={{ ...BASE, total_cost: 6.4 }} />);
    expect(screen.getByTestId("cost-total").textContent).toBe("≈$6.40");
    const note = screen.getByTestId("cost-api-price");
    expect(note.textContent).toBe("at API list price");
    expect(note.getAttribute("title")).toBe("Notional cost at the API price list. A subscription spends window limits, not money");
    expect(note.getAttribute("aria-label")).toContain("A subscription spends window limits, not money");
  });

  it("keeps the incomplete mark after the approximate figure", () => {
    render(<CostQualityBlock summary={{ ...BASE, total_cost: 6.4, cost_incomplete: true }} />);
    expect(screen.getByTestId("cost-total").textContent).toBe("≈$6.40+");
    expect(screen.getByTestId("cost-api-price")).toBeTruthy();
  });

  it("an unknown total stays a dash: no ≈ and no price caption", () => {
    render(<CostQualityBlock summary={{ ...BASE, total_cost: null, cost_incomplete: true }} />);
    expect(screen.getByTestId("cost-total").textContent).toBe("—+");
    expect(screen.queryByTestId("cost-api-price")).toBeNull();
  });
});

// A known zero is "≈$0.00"; "≈<$0.01" is kept for 0 < cost < 0.01 only.
describe("formatCost", () => {
  it("shows a known zero as $0.00, not as a sub-cent figure", () => {
    expect(formatCost(0)).toBe("≈$0.00");
  });

  it("keeps <$0.01 for a positive figure below one cent", () => {
    expect(formatCost(0.004)).toBe("≈<$0.01");
  });

  it("shows an unknown cost as a dash", () => {
    expect(formatCost(null)).toBe("—");
  });

  it("rounds a figure of a cent or more to two decimals", () => {
    expect(formatCost(0.01)).toBe("≈$0.01");
    expect(formatCost(1.5)).toBe("≈$1.50");
  });
});
