import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { TaskCard } from "@/components/task-card";
import type { Task } from "@/types";

const AGENT = "aaaaaaaa-1111-1111-1111-111111111111";
const future = () => new Date(Date.now() + 90 * 60 * 1000).toISOString();

const full = {
  id: "t1",
  title: "Layout card",
  description: "has a body",
  has_description: true,
  priority: "high",
  labels: ["lbl-x"],
  assignee_id: AGENT,
  assignee_type: "agent",
  assignee_name: "Grace",
  due_date: "2031-01-01T00:00:00Z",
  estimated_hours: 3,
  subtask_count: 2,
  artifact_count: 1,
  vcs_link_count: 1,
  vcs_providers: ["github"],
  checked_out_by: AGENT,
  checkout_expires: future(),
  runtime_state: { kind: "working", lane: "Grace", since: "2026-10-10T10:00:00Z", source: "explicit", stale: false },
} as unknown as Task;

const card = (task: Task) => render(<TaskCard task={task} />).container.firstElementChild as HTMLElement;

// Alex's markup of the board card anatomy (24.10): 6 and 7 gone, 18 and 9 take their places,
// 15 gone, 4/5 move to the bottom row, 17 and 20 follow the delegation mark.
describe("board card block order", () => {
  it("draws the counters row under the title, then the runtime row, labels, and the bottom row", () => {
    const el = card(full);
    const [titleRow, counters, runtime, labels, bottom] = Array.from(el.children);
    expect(titleRow!.textContent).toContain("Layout card");
    expect(titleRow!.querySelector("svg.lucide-git-branch")).toBeNull(); // Alex 25.10: not beside the title
    expect(counters!.textContent).toMatch(/2/);
    expect(counters!.querySelector("svg.lucide-git-branch")).not.toBeNull();
    expect(runtime!.textContent).toMatch(/Grace/);
    expect(labels!.textContent).toBe("lbl-x");
    expect(bottom).toBe(el.lastElementChild);
    expect(el.children).toHaveLength(5);
  });

  it("bottom row: assignee, priority, due date, then VCS and attachments at the end", () => {
    const bottom = card(full).lastElementChild as HTMLElement;
    const at = (sel: string) => Array.from(bottom.querySelectorAll("*")).findIndex((n) => n.matches(sel));
    const due = Array.from(bottom.querySelectorAll("span")).findIndex((n) => /2031|year|in /.test(n.textContent ?? "") && !n.querySelector("span"));
    const vcs = at('[aria-label*="VCS" i]');
    const clip = at("svg.lucide-paperclip");
    expect(due).toBeGreaterThanOrEqual(0);
    const est = Array.from(bottom.querySelectorAll("span")).findIndex((n) => n.textContent === "3h");
    expect(est).toBeGreaterThan(due);
    expect(vcs).toBeGreaterThan(-1);
    expect(clip).toBeGreaterThan(vcs);
    expect(bottom.querySelector('[aria-label*="VCS" i]')!.parentElement!.className).toContain("ml-auto");
  });

  it("drops the description mark, the lock hand and the pencil", () => {
    const el = card(full);
    expect(el.querySelector('[data-testid="checkout-indicator"]')).toBeNull();
    expect(el.querySelector("svg.lucide-hand, svg.lucide-align-left, svg.lucide-pencil")).toBeNull();
  });

  it("keeps the VCS mark out of the title row", () => {
    const el = card(full);
    const mark = el.querySelector('[aria-label*="VCS" i]')!;
    expect(mark).not.toBeNull();
    expect(el.firstElementChild!.contains(mark)).toBe(false);
  });
});
