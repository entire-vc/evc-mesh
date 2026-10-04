import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router";
import { ProjectSettingsPage } from "@/pages/project-settings";
import { useProjectStore } from "@/stores/project";
import { useRecurringStore } from "@/stores/recurring";
import { api } from "@/lib/api";
import type { Project, RecurringSchedule } from "@/types";

vi.mock("@/lib/api", async () => ({ ...await vi.importActual("@/lib/api"), api: vi.fn() }));
const schedule = { id: "schedule-1", project_id: "project-1", title_template: "Weekly audit",
  frequency: "weekly", priority: "low", assignee_type: "unassigned", is_active: true,
  instance_count: 0 } as RecurringSchedule;
function Navigation() {
  const location = useLocation();
  const navigate = useNavigate();
  return <><output data-testid="location">{location.search}</output>
    <button onClick={() => navigate(-1)}>Back</button></>;
}
function show(query: string) {
  return render(<MemoryRouter initialEntries={[`/w/ws/p/project/settings${query}`]}>
    <Navigation /><Routes><Route path="/w/:wsSlug/p/:projectSlug/settings" element={<ProjectSettingsPage />} /></Routes>
  </MemoryRouter>);
}
describe("project settings deep link", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.mocked(api).mockImplementation(async path => /\/(statuses|custom-fields)$/.test(path)
      ? [] : { items: path.endsWith('/recurring') ? [schedule] : [], has_more: false });
    HTMLElement.prototype.scrollIntoView = vi.fn();
    useProjectStore.setState({ statuses: [], currentProject: { id: "project-1", slug: "project", name: "Project", settings: {} } as Project });
    useRecurringStore.setState({ schedules: [], schedulesProjectId: null, schedulesError: false, schedulesLoading: false });
  });
  it("opens Recurring and selects the named schedule; back navigation restores the URL tab", async () => {
    const { container } = show("?tab=recurring&schedule=schedule-1&keep=1");
    await screen.findByText("Weekly audit");
    expect(container.querySelector('[data-schedule-id="schedule-1"]')).toHaveAttribute("data-focused", "true");
    expect(screen.getByRole("link", { name: "Link to schedule" })).toHaveAttribute("href", "/w/ws/p/project/settings?tab=recurring&schedule=schedule-1");
    fireEvent.click(screen.getByRole("button", { name: "General" }));
    expect(screen.getByTestId("location")).toHaveTextContent("?tab=general&keep=1");
    expect(screen.queryByText("Weekly audit")).not.toBeInTheDocument();
    fireEvent.click(screen.getByText("Back", { exact: true }));
    await screen.findByText("Weekly audit");
    expect(container.querySelector('[data-schedule-id="schedule-1"]')).toHaveAttribute("data-focused", "true");
  });
  it("shows not found for a nonexistent schedule after loading", async () => {
    show("?tab=recurring&schedule=missing");
    expect(await screen.findByText(/Schedule not found/)).toBeInTheDocument();
    expect(screen.getByText("Weekly audit")).toBeInTheDocument();
  });
  it("shows transport errors separately from nonexistent IDs", async () => {
    vi.mocked(api).mockRejectedValue(new Error("unavailable"));
    show("?tab=recurring&schedule=missing");
    expect(await screen.findByText(/Could not load recurring schedules/)).toBeInTheDocument();
    expect(screen.queryByText(/Schedule not found/)).not.toBeInTheDocument();
  });
  it("uses General for an unknown tab", async () => {
    show("?tab=unknown");
    await waitFor(() => expect(screen.queryByText("Recurring Schedules")).not.toBeInTheDocument());
    expect(screen.getByText("General", { exact: true })).toBeInTheDocument();
  });
  it("does not allow an old project response to replace current schedules", async () => {
    let finishOld!: (data: unknown) => void;
    vi.mocked(api).mockImplementation(path => path.includes("old-project")
      ? new Promise(resolve => { finishOld = resolve; }) : Promise.resolve({ items: [schedule], has_more: false }));
    const old = useRecurringStore.getState().fetchSchedules("old-project");
    await useRecurringStore.getState().fetchSchedules("project-1", "schedule-1");
    finishOld({ items: [], has_more: false });
    await old;
    expect(useRecurringStore.getState().schedulesProjectId).toBe("project-1");
    expect(useRecurringStore.getState().schedules).toEqual([schedule]);
  });
});
