import { beforeEach, describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ArtifactList } from "@/components/artifact-list";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import type { Project, Workspace } from "@/types";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";

// R6: artifact-list previously offered "browse files" and "attach Obsidian
// doc" with no way to link one of this project's own Docs. This exercises
// the new third source end to end — search, pick, insert.

const workspace: Workspace = {
  id: "ws-1",
  name: "Entire VC",
  slug: "entire-vc",
  owner_id: "user-1",
  settings: {},
  billing_plan_id: "",
  billing_customer_id: "",
  icon_url: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const project: Project = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Mesh",
  description: "",
  slug: "mesh-dev",
  icon: "",
  settings: {},
  default_assignee_type: "agent",
  is_archived: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("ArtifactList — unified attachment source picker (R6)", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    useWorkspaceStore.setState({ currentWorkspace: workspace });
    useProjectStore.setState({ projects: [project], currentProject: project });
  });

  it("links a Doc, inserting a real markdown link — parity with what documentHref/linkLabel produce for the same target elsewhere in the app", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-1/artifacts") {
        return Promise.resolve({ items: [], total: 0 });
      }
      if (path === "/api/v1/projects/proj-1/integrations/team-relay") {
        return Promise.resolve({ enabled: false });
      }
      if (path === "/api/v1/projects/proj-1/documents/search") {
        return Promise.resolve({
          items: [
            {
              id: "doc-42",
              title: "Deploy runbook",
              snippet: "how to deploy",
              snippet_is_match: true,
            },
          ],
        });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    const onDocInsert = vi.fn();
    render(
      <MemoryRouter>
        <ArtifactList taskId="task-1" projId="proj-1" onDocInsert={onDocInsert} />
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole("button", { name: /attach/i }));
    fireEvent.click(screen.getByText("From Docs"));
    fireEvent.change(screen.getByPlaceholderText("Search documents..."), {
      target: { value: "runbook" },
    });

    const hit = await screen.findByText("Deploy runbook");
    fireEvent.click(hit);

    await waitFor(() => {
      expect(onDocInsert).toHaveBeenCalledWith(
        "[Deploy runbook](/w/entire-vc/p/mesh-dev/docs/doc-42)",
      );
    });
  });

  it("links an Obsidian doc as a bare relay:// URL, unchanged from the pre-R6 behaviour", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-1/artifacts") {
        return Promise.resolve({ items: [], total: 0 });
      }
      if (path === "/api/v1/projects/proj-1/integrations/team-relay") {
        return Promise.resolve({ enabled: true });
      }
      if (path === "/api/v1/projects/proj-1/tr/search") {
        return Promise.resolve({
          docs: [
            {
              title: "Meeting notes",
              path: "notes/meeting.md",
              relay_url: "relay://mesh/notes/meeting.md",
            },
          ],
        });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    const onDocInsert = vi.fn();
    render(
      <MemoryRouter>
        <ArtifactList taskId="task-1" projId="proj-1" onDocInsert={onDocInsert} />
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole("button", { name: /attach/i }));
    fireEvent.click(await screen.findByText("From Obsidian"));

    const hit = await screen.findByText("Meeting notes");
    fireEvent.click(hit);

    await waitFor(() => {
      expect(onDocInsert).toHaveBeenCalledWith("relay://mesh/notes/meeting.md");
    });
  });

  it("does not offer Obsidian when the project has no Team Relay integration", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-1/artifacts") {
        return Promise.resolve({ items: [], total: 0 });
      }
      if (path === "/api/v1/projects/proj-1/integrations/team-relay") {
        return Promise.resolve({ enabled: false });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    render(
      <MemoryRouter>
        <ArtifactList taskId="task-1" projId="proj-1" onDocInsert={vi.fn()} />
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole("button", { name: /attach/i }));
    expect(screen.getByText("From Docs")).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.queryByText("From Obsidian")).not.toBeInTheDocument();
    });
  });
});
