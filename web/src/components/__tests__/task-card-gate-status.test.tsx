import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TaskCard, humanGateTitle } from "../task-card";
import type { Task } from "@/types";

const t = (over: Partial<Task>) =>
  ({ id: "1", title: "Gated", priority: "medium", human_gate: true, human_gate_class: "hard", ...over }) as unknown as Task;

describe("task card gate in Triage", () => {
  it("shows real status and the gate reason in the tooltip", () => {
    render(<TaskCard task={t({ gate_reason: "need budget decision" })} gateStatusName="In Progress" />);
    expect(screen.getByTestId("human-gate-status").textContent).toContain("In Progress");
    const ind = screen.getByTestId("human-gate-indicator");
    expect(ind.getAttribute("title")).toContain("hard");
    expect(ind.getAttribute("title")).toContain("need budget decision");
  });

  it("without a relocation the card keeps the plain lock and gains class and reason", () => {
    render(<TaskCard task={t({ human_gate_class: "soft", gate_reason: "why" })} />);
    expect(screen.queryByTestId("human-gate-status")).toBeNull();
    expect(humanGateTitle(t({ human_gate_class: "soft", gate_reason: "why" }))).toContain("soft");
    expect(screen.getByTestId("human-gate-indicator").getAttribute("title")).toContain("why");
  });
});
