import { describe, it, expect } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { AgentSessionCard, CostTrackingSection } from "@/pages/session-dashboard";
import type { AnalyticsMetrics } from "@/types";

type CostMetrics = AnalyticsMetrics["cost_metrics"];

const SAMPLE_COST: CostMetrics = {
  total_cost: 12.5,
  total_tokens_in: 150_000,
  total_tokens_out: 45_000,
  session_count: 8,
  reported_session_count: 8,
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

// ── #6b322896: long status/task-title text must stay fully readable ──
// The JSX used to slice heartbeat_message at 80 and current_task_title at
// 60 chars, so the tail of the string never reached the DOM and no CSS or
// interaction could recover it. These fixtures mirror the live Gandalf card
// that surfaced the defect (status >80, title >60).

const LONG_STATUS =
	"Deploy drift check: live backend sits six commits behind canon HEAD; the oldest undeployed commit carries migrations/00124_add_tags.sql and must ship via build:mesh-backend before the watchdog pages the on-call again";

const LONG_TITLE =
	"[UI mobile] Sessions: full status and task title unreachable after truncation to 60-80 chars in the agent session cards";

type AgentCardFixture = React.ComponentProps<typeof AgentSessionCard>["agent"];

const BASE_AGENT: AgentCardFixture = {
	id: "agent-1",
	name: "Gandalf",
	status: "busy",
	last_heartbeat_at: "2026-10-03T18:20:20Z",
	seconds_since_heartbeat: 30,
	is_stale: false,
	heartbeat_message: LONG_STATUS,
	current_task_id: "task-42",
	current_task_title: LONG_TITLE,
};

function renderAgentCard(overrides: Partial<AgentCardFixture> = {}) {
	return render(
		<MemoryRouter>
			<AgentSessionCard agent={{ ...BASE_AGENT, ...overrides }} />
		</MemoryRouter>,
	);
}

describe("AgentSessionCard — long text stays fully readable (#6b322896)", () => {
	it("renders the FULL status and title strings (no JSX slicing)", () => {
		renderAgentCard();
		// Status renders once (the expand button); the title renders twice —
		// the mobile expand button and the desktop link carry the same text.
		expect(screen.getAllByText(LONG_STATUS)).toHaveLength(1);
		expect(screen.getAllByText(LONG_TITLE)).toHaveLength(2);
	});

	it("exposes an expand control per long row: aria-expanded toggles line-clamp-2 off and back", () => {
		renderAgentCard();

		for (const name of [LONG_STATUS, LONG_TITLE]) {
			const btn = screen.getByRole("button", { name });
			expect(btn).toHaveAttribute("aria-expanded", "false");
			// The clamp is on the inner span, never on the button itself.
			expect(btn.className).not.toContain("line-clamp-2");
			expect(btn.querySelector("span.line-clamp-2")).not.toBeNull();

			fireEvent.click(btn);
			expect(btn).toHaveAttribute("aria-expanded", "true");
			expect(btn.querySelector("span.line-clamp-2")).toBeNull();
			// Full text is still in the DOM in both states.
			expect(screen.getAllByText(name).length).toBeGreaterThanOrEqual(1);

			fireEvent.click(btn);
			expect(btn).toHaveAttribute("aria-expanded", "false");
			expect(btn.querySelector("span.line-clamp-2")).not.toBeNull();
		}
	});

	it("keeps task navigation: the title link and a mobile open-task link point at /t/:id", () => {
		renderAgentCard();
		expect(screen.getByRole("link", { name: LONG_TITLE })).toHaveAttribute("href", "/t/task-42");
		expect(screen.getByRole("link", { name: "Open task" })).toHaveAttribute("href", "/t/task-42");
	});

	it("caps absurd strings at 500 chars with an ellipsis (no 80/60 data loss)", () => {
		const huge = "s".repeat(500) + "MARKERXYZ";
		renderAgentCard({ heartbeat_message: huge, current_task_title: huge });

		// Everything past the cap never reaches the DOM.
		expect(screen.queryByText(/MARKERXYZ/)).not.toBeInTheDocument();
		// Both long rows (status + mobile task title) carry the capped text.
		const btns = screen.getAllByRole("button", { name: /^s{500}…$/ });
		expect(btns).toHaveLength(2);
		for (const btn of btns) expect(btn.textContent).toHaveLength(501);
	});

	it("short rows stay plain text/links — no expand buttons", () => {
		renderAgentCard({ heartbeat_message: "All good", current_task_title: "Small fix" });

		expect(screen.queryByRole("button")).toBeNull();
		expect(screen.getByText("All good")).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Small fix" })).toHaveAttribute("href", "/t/task-42");
	});

	// jsdom cannot apply Tailwind, so a CSS `display` override that silently
	// kills line-clamp-2 on mobile is invisible to the tests above — exactly
	// what the independent review of the first cut caught. Vitest stubs CSS
	// imports (even `?raw` — it resolves to ""), so read the stylesheet from
	// disk, keyed off this test file's own location.
	it("mobile button CSS never sets display — line-clamp-2 must keep -webkit-box", () => {
		const cssPath = join(dirname(expect.getState().testPath as string), "../../index.css");
		const block = readFileSync(cssPath, "utf8").match(/\.mesh-mobile-sessions button \{([^}]*)\}/);
		expect(block).not.toBeNull();
		expect(block?.[1]).not.toMatch(/display\s*:/);
	});

	// Rework ratchets (review of the first cut caught both live at 393/1440;
	// jsdom cannot see Tailwind, so assert on the class/selector strings).
	it("desktop title link is hidden below md — no always-expanded duplicate on mobile", () => {
		renderAgentCard();
		const desktop = screen.getByRole("link", { name: LONG_TITLE });
		// Both display classes must be !important: a plain `hidden` loses to
		// the mobile-scope rule `.mesh-mobile-sessions a { display:
		// inline-flex }` (0,1,1 > 0,1,0) — the desktop copy rendered beside
		// the expand button at 393 WITH the hidden class present; and a plain
		// `md:inline-block` then loses to our own `hidden!` everywhere, which
		// killed the link on desktop entirely. Both found live in the
		// harness; jsdom cannot see the cascade, so assert the class string.
		expect(desktop.className).toMatch(/(^|\s)hidden!(\s|$)/);
		expect(desktop.className).toMatch(/(^|\s)md:inline-block!(\s|$)/);
		// The truncate combo that restores the "…" at the card edge on ≥768.
		expect(desktop.className).toMatch(/(^|\s)md:truncate(\s|$)/);
		expect(desktop.className).toMatch(/(^|\s)md:max-w-full(\s|$)/);
	});

	// Ratchet for the third-line sliver (caught live at 393 in review, twice):
	// Blink paints the top of line 3 into any clamp box taller than two
	// lines — and the touch-area audit makes every button ≥44px. So the clamp
	// must never sit on the button; an auto-height inner span is cut at
	// exactly two lines. jsdom cannot see painting, so assert the structure.
	it("collapsed rows clamp on an auto-height span inside the button, never on the button itself", () => {
		renderAgentCard();
		for (const name of [LONG_STATUS, LONG_TITLE]) {
			const btn = screen.getByRole("button", { name });
			expect(btn.className).not.toContain("line-clamp-2");
			const span = btn.querySelector("span.line-clamp-2");
			expect(span).not.toBeNull();
			// The span is auto-height: no height/min-height utility sneaks in
			// and re-creates the taller-than-two-lines clip box.
			expect(span?.className).not.toMatch(/(^|\s)(h-|min-h-|max-h-)/);
		}
	});
});

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
    // Avg / reported session: $12.50 / 8 reported = $1.56.
    expect(screen.getByText("$1.56")).toBeInTheDocument();
    expect(screen.getByText("8 of 8 sessions reported (100%)")).toBeInTheDocument();

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

  it("renders '—' instead of $0.00 when no session in the window has reported cost", () => {
    renderWithRouter(
      <CostTrackingSection
        cost={{ ...SAMPLE_COST, reported_session_count: 0 }}
        isLoading={false}
        wsSlug="acme"
      />,
    );

    // Sessions still shows the real (unreported) count — the section is not "empty".
    expect(screen.getByText("8")).toBeInTheDocument();
    expect(screen.getByText("0 of 8 sessions reported (0%)")).toBeInTheDocument();
    // Both cost figures render "—", never $0.00 — unknown must not be presented as zero.
    expect(screen.getAllByText("—")).toHaveLength(2);
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
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
