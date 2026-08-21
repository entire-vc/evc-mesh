import { beforeEach, describe, expect, it } from "vitest";
import { loadBoardFilters, saveBoardFilters, type BoardFilterState } from "@/lib/board-view-storage";

const sample: BoardFilterState = {
  groupBy: "priority",
  sortBy: "due_date",
  showClosed: true,
  showSubtasks: true,
  humanGateOnly: true,
  searchQuery: "urgent fix",
  priorityFilter: "high",
  assigneeFilter: "agent",
  customFieldFilters: { severity: "P1" },
  selectedTags: ["bug", "prod"],
  cfFilters: { owner: { type: "text", textValue: "riker" } },
  assigneeIdsFilter: ["agent-1"],
};

beforeEach(() => {
  localStorage.clear();
});

describe("loadBoardFilters / saveBoardFilters", () => {
  it("round-trips a saved filter state", () => {
    saveBoardFilters("proj-a", sample);
    expect(loadBoardFilters("proj-a")).toEqual(sample);
  });

  it("returns null when nothing was ever saved for the project", () => {
    expect(loadBoardFilters("never-saved")).toBeNull();
  });

  it("keeps different projects' filters independent", () => {
    saveBoardFilters("proj-a", sample);
    saveBoardFilters("proj-b", { ...sample, groupBy: "assignee" });

    expect(loadBoardFilters("proj-a")?.groupBy).toBe("priority");
    expect(loadBoardFilters("proj-b")?.groupBy).toBe("assignee");
  });

  it("degrades to null on corrupted storage instead of throwing", () => {
    localStorage.setItem("mesh_board_filters_proj-a", "{not json");
    expect(loadBoardFilters("proj-a")).toBeNull();
  });

  it("degrades to null when the stored value isn't an object", () => {
    localStorage.setItem("mesh_board_filters_proj-a", JSON.stringify("just a string"));
    expect(loadBoardFilters("proj-a")).toBeNull();
  });
});
