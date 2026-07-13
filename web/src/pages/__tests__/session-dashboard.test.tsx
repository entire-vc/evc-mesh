import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { CostTrackingSection } from "@/pages/session-dashboard";
import type { AnalyticsMetrics } from "@/types";

type CostMetrics = AnalyticsMetrics["cost_metrics"];

const SAMPLE_COST: CostMetrics = {
  total_cost: 12.5,
  total_tokens_in: 150_000,
  total_tokens_out: 45_000,
  session_count: 8,
  by_agent: [
    { agent_id: "a1", agent_name: "Linus", cost: 8, tokens_in: 100_000, tokens_out: 30_000 },
    { agent_id: "a2", agent_name: "Garfield", cost: 4.5, tokens_in: 50_000, tokens_out: 15_000 },
  ],
  by_project: [],
  by_day: [],
  top_tasks: [
    {
      task_id: "t1",
      task_title: "Wire up cost dashboard",
      cost: 8,
      tokens_in: 100_000,
      tokens_out: 30_000,
      session_count: 3,
    },
  ],
};

function renderWithRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

describe("CostTrackingSection", () => {
  it("shows the session_report onboarding hint when there is no data", () => {
    renderWithRouter(<CostTrackingSection cost={null} isLoading={false} />);
    expect(screen.getByText(/No cost data reported yet/i)).toBeInTheDocument();
  });

  it("renders KPI totals, agent breakdown, and top tasks when data is present", () => {
    renderWithRouter(<CostTrackingSection cost={SAMPLE_COST} isLoading={false} wsSlug="acme" />);

    expect(screen.getByText("$12.50")).toBeInTheDocument();
    expect(screen.getByText("150.0k / 45.0k")).toBeInTheDocument();
    expect(screen.getByText("8")).toBeInTheDocument();

    expect(screen.getByText("Linus")).toBeInTheDocument();
    expect(screen.getByText("Garfield")).toBeInTheDocument();
    // $8.00 appears twice: agent-A's spend and the (coincidentally equal-cost) top task.
    expect(screen.getAllByText("$8.00")).toHaveLength(2);
    expect(screen.getByText("$4.50")).toBeInTheDocument();

    const taskLink = screen.getByRole("link", { name: "Wire up cost dashboard" });
    expect(taskLink).toHaveAttribute("href", "/t/t1");

    const analyticsLink = screen.getByRole("link", { name: /View full analytics/ });
    expect(analyticsLink).toHaveAttribute("href", "/w/acme/analytics");
  });

  it("treats a zero-session response as empty even when cost object is present", () => {
    renderWithRouter(
      <CostTrackingSection
        cost={{ ...SAMPLE_COST, session_count: 0, by_agent: [], top_tasks: [] }}
        isLoading={false}
      />,
    );
    expect(screen.getByText(/No cost data reported yet/i)).toBeInTheDocument();
  });
});
