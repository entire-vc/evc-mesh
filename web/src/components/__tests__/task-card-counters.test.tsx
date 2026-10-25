import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { TaskCard } from "@/components/task-card";
import type { Task } from "@/types";

const base = { id: "t1", title: "Counter card", priority: "none", labels: [], assignee_type: "unassigned" };
const card = (extra: Record<string, unknown>) =>
  render(<TaskCard task={{ ...base, ...extra } as unknown as Task} />).container.firstElementChild as HTMLElement;
// Alex 25.10: the counters are their own row right under the title, not beside it.
const titleRow = (el: HTMLElement) => el.firstElementChild!.nextElementSibling as HTMLElement;

// Alex 24.10: «14 (1)», the bracket red; dependencies beside it as «5 (1)».
describe("board card subtask and dependency counters", () => {
  it("subtasks read «14 (1)» with the open count in the destructive tone and no word", () => {
    const row = titleRow(card({ subtask_count: 14, open_subtask_count: 1 }));
    expect(row.textContent).toContain("14(1)");
    expect(row.textContent).not.toMatch(/subtask|подзадач/i);
    const open = Array.from(row.querySelectorAll("span")).find((n) => n.textContent === "(1)");
    expect(open?.className).toContain("text-destructive");
    expect(row.querySelector("svg.lucide-git-branch")).not.toBeNull();
  });

  it("no brackets when nothing is open", () => {
    const row = titleRow(card({ subtask_count: 14, open_subtask_count: 0 }));
    expect(row.textContent).toContain("14");
    expect(row.textContent).not.toContain("(");
  });

  it("dependencies draw a link icon and «5 (1)» right after the subtasks", () => {
    const row = titleRow(card({ subtask_count: 14, open_subtask_count: 1, dependency_count: 5, open_dependency_count: 1 }));
    expect(row.textContent).toContain("14(1)5(1)");
    expect(row.querySelector("svg.lucide-link-2")).not.toBeNull();
    const reds = Array.from(row.querySelectorAll("span.text-destructive")).map((n) => n.textContent);
    expect(reds).toEqual(["(1)", "(1)"]);
  });

  it("dependencies with all blockers closed: «5» without brackets, shown even without subtasks", () => {
    const row = titleRow(card({ dependency_count: 5, open_dependency_count: 0 }));
    expect(row.textContent).toContain("5");
    expect(row.textContent).not.toContain("(");
    expect(row.querySelector("svg.lucide-git-branch")).toBeNull();
    expect(row.querySelector("svg.lucide-link-2")).not.toBeNull();
  });

  it("counters sit in their own row under the title; a bare card draws no such row", () => {
    const el = card({ subtask_count: 2, open_subtask_count: 2, dependency_count: 2, open_dependency_count: 2 });
    expect(el.firstElementChild!.querySelector("svg.lucide-git-branch, svg.lucide-link-2")).toBeNull();
    expect(el.firstElementChild!.nextElementSibling!.textContent).toContain("2(2)2(2)");
    expect(card({}).children).toHaveLength(2);
  });

  it("zero dependencies draw no link icon; a bare card draws no counters", () => {
    expect(titleRow(card({ subtask_count: 3 })).querySelector("svg.lucide-link-2")).toBeNull();
    const bare = titleRow(card({}));
    expect(bare.querySelector("svg.lucide-git-branch, svg.lucide-link-2")).toBeNull();
  });
});
