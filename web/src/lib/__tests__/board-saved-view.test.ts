import { describe, expect, it } from "vitest";
import {
  buildBoardSavedViewState,
  isBoardFiltersAtDefault,
  readBoardSavedViewState,
  type BoardSavedViewState,
} from "@/lib/board-saved-view";

const fullState: BoardSavedViewState = {
  searchQuery: "urgent fix",
  priorityFilter: "high",
  assigneeFilter: "agent",
  assigneeIdsFilter: ["user-1", "agent-7", "unassigned"],
  cfFilters: { owner: { type: "text", textValue: "riker" } },
  selectedTags: ["bug", "prod"],
  groupBy: "priority",
  showClosed: true,
  showSubtasks: true,
  sortBy: "due_date",
};

describe("board saved-view capture/restore", () => {
  it("round-trips the full view state", () => {
    const payload = buildBoardSavedViewState(fullState);
    const restored = readBoardSavedViewState(payload.filters, payload.sortBy);

    expect(restored).toEqual(fullState);
  });

  // The bug 88bfde8 fixed: state selectable in the toolbar but silently
  // dropped on save. Guard every key rather than a hand-picked few, so a new
  // filter added to the capture side can't skip the restore side.
  it("captures every key of the board view state", () => {
    const { filters, sortBy } = buildBoardSavedViewState(fullState);

    expect(filters).toEqual({
      search: "urgent fix",
      priority: "high",
      assignee: "agent",
      assignee_ids: ["user-1", "agent-7", "unassigned"],
      custom_fields: { owner: { type: "text", textValue: "riker" } },
      tags: ["bug", "prod"],
      group_by: "priority",
      show_closed: true,
      show_subtasks: true,
    });
    expect(sortBy).toBe("due_date");
  });

  it("keeps the assignee-ids filter in the saved payload", () => {
    const { filters } = buildBoardSavedViewState(fullState);
    expect(filters.assignee_ids).toEqual(["user-1", "agent-7", "unassigned"]);
    expect(readBoardSavedViewState(filters).assigneeIdsFilter).toEqual([
      "user-1",
      "agent-7",
      "unassigned",
    ]);
  });

  it("falls back to defaults for a legacy view saved before full capture", () => {
    // Views written by the old code carried only search/priority/assignee.
    const restored = readBoardSavedViewState({
      search: "old",
      priority: "low",
      assignee: "user",
    });

    expect(restored).toEqual({
      searchQuery: "old",
      priorityFilter: "low",
      assigneeFilter: "user",
      assigneeIdsFilter: [],
      cfFilters: {},
      selectedTags: [],
      groupBy: "status",
      showClosed: false,
      showSubtasks: false,
    });
  });

  it("omits sortBy when the view carries no sort_by, so the active sort is kept", () => {
    expect(readBoardSavedViewState({}).sortBy).toBeUndefined();
    expect(readBoardSavedViewState({}, null).sortBy).toBeUndefined();
  });

  it("ignores an unknown group_by or sort_by instead of applying it", () => {
    const restored = readBoardSavedViewState({ group_by: "nonsense" }, "nonsense");
    expect(restored.groupBy).toBe("status");
    expect(restored.sortBy).toBeUndefined();
  });

  it("tolerates non-array tags and assignee_ids", () => {
    const restored = readBoardSavedViewState({ tags: "bug", assignee_ids: 42 });
    expect(restored.selectedTags).toEqual([]);
    expect(restored.assigneeIdsFilter).toEqual([]);
  });
});

describe("isBoardFiltersAtDefault (Reset filters menu item's disabled state)", () => {
  it("is true for an empty payload (nothing saved yet / no filters set)", () => {
    expect(isBoardFiltersAtDefault({})).toBe(true);
  });

  it("is true when group_by/sort_by are non-default but every filter is", () => {
    // Reset filters must not disable itself off changes to layout-only state.
    const { filters } = buildBoardSavedViewState({
      ...fullState,
      searchQuery: "",
      priorityFilter: "all",
      assigneeFilter: "all",
      assigneeIdsFilter: [],
      cfFilters: {},
      selectedTags: [],
      showClosed: false,
      showSubtasks: false,
    });
    expect(isBoardFiltersAtDefault(filters)).toBe(true);
  });

  it.each([
    ["searchQuery", { search: "bug" }],
    ["priorityFilter", { priority: "high" }],
    ["assigneeFilter", { assignee: "agent" }],
    ["assigneeIdsFilter", { assignee_ids: ["user-1"] }],
    ["selectedTags", { tags: ["prod"] }],
    ["showClosed", { show_closed: true }],
    ["showSubtasks", { show_subtasks: true }],
  ])("is false when %s differs from default", (_name, filters) => {
    expect(isBoardFiltersAtDefault(filters)).toBe(false);
  });

  it("is false when a custom-field filter is active", () => {
    expect(
      isBoardFiltersAtDefault({
        custom_fields: { owner: { type: "text", textValue: "riker" } },
      }),
    ).toBe(false);
  });

  it("is true when a custom-field entry exists but carries no active value", () => {
    // Matches countActiveCFFilters semantics: an entry with type "select" and
    // selectValue "all" (or unset) isn't an active filter.
    expect(
      isBoardFiltersAtDefault({
        custom_fields: { owner: { type: "select", selectValue: "all" } },
      }),
    ).toBe(true);
  });
});
