import { describe, expect, it } from "vitest";
import { blockerSummary, mergeGraphPages, type DependencyGraph, type DependencyNode } from "@/lib/dependency-graph";

const node = (id: string, extra = {}): DependencyNode => ({ id, availability: "available", title: id,
  status_category: "todo", blockers: 0, unfinished_blockers: 0, unknown_blockers: 0, dependents: 0, ...extra });
const branch = (ids: string[] = []) => ({ ids, total_count: ids.length, completed_count: 0, unknown_count: 0, page: 1, has_more: false });
const graph = (): DependencyGraph => ({ mode: "focus", focus: "center", tasks: [node("center")], dependencies: [],
  before: branch(), after: branch(), relations: branch(), total_count: 1, scope_total_count: 1, completed_count: 0,
  unknown_count: 0, page: 1, page_size: 10, has_more: false, complete: true, visibility_complete: true,
  revision: "r1", cycle_ids: [], cycle_count: 0 });

describe("dependency facts and continuation", () => {
  it("does not claim absence of unfinished blockers on unknown, collapsed, or partial incoming data", () => {
    const g = graph();
    expect(blockerSummary(g.tasks[0]!, g)).toBe("No unfinished direct blockers");
    expect(blockerSummary(node("center", { unknown_blockers: 1 }), g)).toBe("There is an unavailable dependency");
    expect(blockerSummary(g.tasks[0]!, { ...g, visibility_complete: false })).toBe("Dependency check is incomplete");
    expect(blockerSummary(g.tasks[0]!, { ...g, before: { ...g.before, has_more: true } })).toBe("Dependency check is incomplete");
    expect(blockerSummary(g.tasks[0]!, { ...g, before: { ...g.before, total_count: 1, completed_count: 1 } })).toBe("Dependency check is incomplete");
    expect(blockerSummary(node("center", { unfinished_blockers: 3 }), g)).toBe("3 unfinished direct blockers");
    expect(blockerSummary(node("center", { availability: "restricted" }), g)).toBe("There is an unavailable dependency");
  });
  it("merges each directed branch without duplicate identities or synthetic blocking edges", () => {
    const first = graph();
    first.tasks.push(node("shared")); first.before = branch(["shared"]); first.after = branch(["shared"]);
    first.dependencies = [{ id: "edge", task_id: "center", depends_on_task_id: "shared", dependency_type: "blocks" }];
    const next = { ...first, tasks: [node("center"), node("extra")], before: { ...branch(["extra"]), page: 2, total_count: 2 } };
    const result = mergeGraphPages(first, next);
    expect(result.tasks.map(t => t.id)).toEqual(["center", "shared", "extra"]);
    expect(result.before.ids).toEqual(["shared", "extra"]);
    expect(result.after.ids).toEqual(["shared"]);
    expect(result.dependencies).toHaveLength(1);
    expect(result.dependencies[0]!.depends_on_task_id).toBe("shared");
    expect(() => mergeGraphPages(first, { ...next, revision: "r2" })).toThrow(/revision/i);
  });
});
