import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { CostQualityBlock } from "@/components/cost-quality-block";
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
// and a plain report with a real zero looks exactly as before.
describe("CostQualityBlock unknown usage", () => {
  it("shows no mark for a complete total", () => {
    render(<CostQualityBlock summary={BASE} />);
    expect(screen.queryByTestId("incomplete-mark")).toBeNull();
    expect(screen.getByTestId("cost-total").textContent).toBe("$1.50");
  });

  it("keeps a real zero as $0 style value without a mark", () => {
    render(<CostQualityBlock summary={{ ...BASE, total_cost: 0, tokens_in: 0, tokens_out: 0 }} />);
    expect(screen.queryByTestId("incomplete-mark")).toBeNull();
    expect(screen.getByTestId("cost-total").textContent).toBe("<$0.01");
    expect(screen.getByTestId("cost-tokens").textContent).toContain("0 / 0");
  });

  it("marks only the measure that is incomplete", () => {
    render(<CostQualityBlock summary={{ ...BASE, tokens_in_incomplete: true }} />);
    const marks = screen.getAllByTestId("incomplete-mark");
    expect(marks).toHaveLength(1);
    expect(screen.getByTestId("cost-tokens").textContent).toMatch(/2\.0k\+ \/ 500/);
    expect(screen.getByTestId("cost-total").textContent).toBe("$1.50");
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
