import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

// The list is paginated by the server (50 per page) and sorted by the table
// header (Title, ascending, by default). The server's own default order is
// updated_at DESC, so a list that asked for page 1 without its sort got the 50
// most recently updated tasks and then sorted only those by title: "Title A→Z"
// showed tasks 011–060 and task 001 was nowhere (perf-counters view.switch,
// job 450822). The mock pages like the real handler does.
const TASKS = Array.from({ length: 60 }, (_, i) => {
  const n = String(i + 1).padStart(3, "0");
  return {
    id: `t${n}`, project_id: "p1", title: `Task ${n}`, status_id: "s1", position: i,
    priority: "medium", labels: [], updated_at: new Date(Date.UTC(2026, 0, 1, 0, 0, i)).toISOString(),
  };
});
const requests: Record<string, unknown>[] = [];
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string, opts?: { params?: Record<string, unknown> }) => {
      if (/\/projects\/p1\/tasks$/.test(path)) {
        const p = opts?.params ?? {};
        requests.push(p);
        const by = (p.sort_by as string) || "updated_at";
        const dir = by === "updated_at" && !p.sort_dir ? "desc" : (p.sort_dir as string) || "asc";
        const items = [...TASKS].sort((a, b) => {
          const cmp = String(a[by as keyof typeof a]).localeCompare(String(b[by as keyof typeof b]));
          return dir === "desc" ? -cmp : cmp;
        });
        const size = Number(p.page_size ?? 50);
        const page = Number(p.page ?? 1);
        return {
          items: items.slice((page - 1) * size, page * size),
          total_count: items.length, page, per_page: size, has_more: page * size < items.length,
        };
      }
      if (/\/projects\/p1\/statuses$/.test(path)) {
        return [{ id: "s1", project_id: "p1", name: "Todo", slug: "todo", color: "#000", position: 0,
          category: "todo", is_default: true, auto_transition: {} }];
      }
      if (/custom-fields/.test(path)) return [];
      return { items: [] };
    }),
  };
});

import { ListViewPage } from "@/pages/list-view";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";

beforeEach(() => {
  requests.length = 0;
  useProjectStore.setState({ currentProject: { id: "p1", slug: "proj", name: "P" } as never, statuses: [] });
  useTaskStore.setState({ tasks: [], tasksByStatus: {}, isLoading: false });
});

const renderList = () =>
  render(
    <MemoryRouter initialEntries={["/ws/proj/list"]}>
      <Routes><Route path="/:wsSlug/:projectSlug/list" element={<ListViewPage />} /></Routes>
    </MemoryRouter>,
  );

describe("list view: the server pages in the order the table shows", () => {
  it("page 1 of the default Title A→Z view starts with the first title", async () => {
    renderList();
    expect(await screen.findByText("Task 001")).toBeTruthy();
    expect(screen.queryByText("Task 060")).toBeNull();
    expect(requests[requests.length - 1]).toMatchObject({ sort_by: "title", sort_dir: "asc" });
  });

  it("flipping the Title header asks the server for the other end", async () => {
    renderList();
    await screen.findByText("Task 001");
    await act(async () => {
      screen.getByRole("columnheader", { name: /^title/i }).click();
    });
    expect(await screen.findByText("Task 060")).toBeTruthy();
    expect(screen.queryByText("Task 001")).toBeNull();
    expect(requests[requests.length - 1]).toMatchObject({ sort_by: "title", sort_dir: "desc" });
  });
});
