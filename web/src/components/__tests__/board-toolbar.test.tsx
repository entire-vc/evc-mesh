import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { BoardToolbar, type BoardToolbarProps } from "@/components/board-toolbar";

function baseProps(overrides: Partial<BoardToolbarProps> = {}): BoardToolbarProps {
  return {
    groupBy: "status",
    onGroupByChange: vi.fn(),
    sortBy: "manual",
    onSortByChange: vi.fn(),
    showClosed: false,
    onShowClosedChange: vi.fn(),
    showSubtasks: false,
    onShowSubtasksChange: vi.fn(),
    searchQuery: "",
    onSearchQueryChange: vi.fn(),
    priorityFilter: "all",
    onPriorityFilterChange: vi.fn(),
    assigneeFilter: "all",
    onAssigneeFilterChange: vi.fn(),
    assigneeCandidates: [],
    assigneeIdsFilter: [],
    onAssigneeIdsFilterChange: vi.fn(),
    allTags: [],
    selectedTags: [],
    onTagsChange: vi.fn(),
    cfFilters: {},
    onCFFiltersChange: vi.fn(),
    filterableFields: [],
    customFieldFilters: {},
    onCustomFieldFiltersChange: vi.fn(),
    onNewTask: vi.fn(),
    ...overrides,
  };
}

describe("BoardToolbar dropdown triggers", () => {
  it("nests no <button> inside another <button> (Group / Sort triggers)", () => {
    render(<BoardToolbar {...baseProps()} />);
    expect(document.querySelectorAll("button button")).toHaveLength(0);
  });

  it("Group by dropdown opens and selecting an option calls onGroupByChange", () => {
    const onGroupByChange = vi.fn();
    render(<BoardToolbar {...baseProps({ onGroupByChange })} />);

    fireEvent.click(screen.getByRole("button", { name: /Group:\s*Status/i }));
    fireEvent.click(screen.getByText("Priority"));

    expect(onGroupByChange).toHaveBeenCalledWith("priority");
  });

  it("Sort dropdown opens and selecting an option calls onSortByChange", () => {
    const onSortByChange = vi.fn();
    render(<BoardToolbar {...baseProps({ onSortByChange })} />);

    const sortTrigger = screen.getAllByRole("button", { name: /Manual/i })[0];
    if (!sortTrigger) throw new Error("sort trigger not found");
    fireEvent.click(sortTrigger);
    const sortOption = screen.getAllByText("Last Updated")[0];
    if (!sortOption) throw new Error("sort option not found");
    fireEvent.click(sortOption);

    expect(onSortByChange).toHaveBeenCalledWith("updated");
  });
});
