import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { AgentShortTag } from "@/components/agent-short-tag";
import { AssigneeAvatar } from "@/components/assignee-avatar";
import { DocMeta } from "@/components/doc-meta";
import { TaskCard } from "@/components/task-card";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import type { Agent, ProjectDocument, Task } from "@/types";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: () => null }));

const agent = { id: "agent-a", name: "Bender", short_tag: "keep-dev" } as Agent;

describe("agent tags on read surfaces", () => {
  beforeEach(() => {
    useAgentStore.setState({ agents: [agent] });
    useRulesStore.setState({ teamDirectory: null });
  });

  it("resolves an actor's tag by ID, rather than by display name", () => {
    const { container } = render(<span>Bender<AgentShortTag id={agent.id} type="agent" /></span>);
    expect(container.textContent).toBe("Bender · keep-dev");
  });

  it.each([null, ""])("preserves an explicit empty tag (%s), including whitespace", (tag) => {
    const { container } = render(<span>Bender<AgentShortTag id={agent.id} tag={tag} /></span>);
    expect(container.textContent).toBe("Bender");
  });

  it("does not label a human even if the caller supplies an agent tag", () => {
    const { container } = render(<span>Bender<AgentShortTag id={agent.id} type="user" tag="keep-dev" /></span>);
    expect(container.textContent).toBe("Bender");
  });

  it("keeps unknown and untagged actors byte-for-byte unchanged", () => {
    useAgentStore.setState({ agents: [{ ...agent, short_tag: null }] });
    const { container } = render(<span>Bender<AgentShortTag id={agent.id} /><AgentShortTag id="unknown" /></span>);
    expect(container.textContent).toBe("Bender");
  });

  it("adds a tag to avatar titles without changing initials", () => {
    render(<AssigneeAvatar id={agent.id} name={agent.name} type="agent" />);
    expect(screen.getByTitle("Bender · keep-dev")).toHaveTextContent("BE");
  });

  it("shows the reviewer tag on a board card", () => {
    const task = { id: "task-a", title: "Example", priority: "none", labels: [],
      reviewer_id: agent.id, reviewer_type: "agent", reviewer_name: agent.name } as unknown as Task;
    render(<TaskCard task={task} />);
    expect(screen.getByTitle("Bender · keep-dev")).toHaveTextContent("BE");
  });

  it("labels document creator/editor while leaving human creator unchanged", () => {
    const doc = { created_by: agent.id, created_by_type: "agent", created_by_name: "Bender",
      updated_by: agent.id, updated_by_name: "Bender", updated_at: "2026-10-03T12:00:00Z" } as ProjectDocument;
    const { container, rerender } = render(<DocMeta doc={doc} />);
    expect(container.textContent).toContain("Created by Bender · keep-dev");
    expect(container.textContent).toContain("Last updated by Bender · keep-dev");
    rerender(<DocMeta doc={{ ...doc, created_by_type: "user", updated_by: null, updated_by_name: null }} />);
    expect(container.textContent).toContain("Created by Bender");
    expect(container.textContent).not.toContain("keep-dev");
  });
});
