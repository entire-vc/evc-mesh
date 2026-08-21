import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { SavedViewsMenu } from "@/components/saved-views-menu";
import type { SavedView } from "@/types";

const fetchViews = vi.fn();
const createView = vi.fn();
const updateView = vi.fn();
const deleteView = vi.fn();

const views: SavedView[] = [
  {
    id: "view-1",
    project_id: "proj-1",
    name: "My Board",
    view_type: "board",
    is_shared: false,
    filters: {},
    sort_by: "manual",
    sort_order: "asc",
    columns: null,
    created_by: "user-1",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  } as SavedView,
];

vi.mock("@/stores/saved-view-store", () => ({
  useSavedViewStore: () => ({
    views,
    isLoading: false,
    fetchViews,
    createView,
    updateView,
    deleteView,
  }),
}));

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
}));

function renderMenu(onApplyView = vi.fn()) {
  return render(
    <SavedViewsMenu
      projectId="proj-1"
      currentViewType="board"
      onApplyView={onApplyView}
    />,
  );
}

describe("SavedViewsMenu dropdown trigger", () => {
  it("nests no <button> inside another <button>", () => {
    renderMenu();
    expect(document.querySelectorAll("button button")).toHaveLength(0);
  });

  it("opens the menu and applying a saved view calls onApplyView", () => {
    const onApplyView = vi.fn();
    renderMenu(onApplyView);

    fireEvent.click(screen.getByRole("button", { name: "Saved Views" }));
    fireEvent.click(screen.getByText("My Board"));

    expect(onApplyView).toHaveBeenCalledWith(views[0]);
  });
});
