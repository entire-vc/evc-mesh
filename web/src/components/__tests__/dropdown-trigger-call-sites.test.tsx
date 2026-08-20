import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { BoardToolbar, type AssigneeCandidate } from "@/components/board-toolbar";
import { SavedViewsMenu } from "@/components/saved-views-menu";
import { useSavedViewStore } from "@/stores/saved-view-store";

// Regression coverage for the two real call sites named in the bug report
// (board-toolbar.tsx's "Group by"/"Sort within column" triggers and
// saved-views-menu.tsx's "Saved Views" trigger). Both wrap a <Button> in
// `<DropdownMenuTrigger asChild>`; before the fix that produced a <button>
// nested inside the Button's own <button>.

function boardToolbarProps(overrides: Partial<Parameters<typeof BoardToolbar>[0]> = {}) {
  const candidates: AssigneeCandidate[] = [];
  return {
    groupBy: "status" as const,
    onGroupByChange: vi.fn(),
    sortBy: "manual" as const,
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
    assigneeCandidates: candidates,
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

describe("board-toolbar.tsx — Group by / Sort dropdown triggers", () => {
  it("renders no nested <button> markup", () => {
    const { container } = render(<BoardToolbar {...boardToolbarProps()} />);
    expect(container.querySelectorAll("button button")).toHaveLength(0);
  });

  it("Group by trigger opens the menu and selecting an option fires the callback", () => {
    const onGroupByChange = vi.fn();
    render(<BoardToolbar {...boardToolbarProps({ onGroupByChange })} />);

    fireEvent.click(screen.getByText("Group:").closest("button")!);
    const option = screen.getByText("Priority");
    fireEvent.click(option);

    expect(onGroupByChange).toHaveBeenCalledWith("priority");
  });
});

describe("saved-views-menu.tsx — Saved Views trigger", () => {
  it("renders no nested <button> markup and opens on click", () => {
    useSavedViewStore.setState({
      views: [],
      isLoading: false,
      fetchViews: vi.fn().mockResolvedValue(undefined),
    });

    const { container } = render(
      <SavedViewsMenu projectId="p1" currentViewType="board" />,
    );

    expect(container.querySelectorAll("button button")).toHaveLength(0);

    fireEvent.click(screen.getByTitle("Saved Views"));
    expect(screen.getByText("Save current view")).toBeInTheDocument();
  });
});
