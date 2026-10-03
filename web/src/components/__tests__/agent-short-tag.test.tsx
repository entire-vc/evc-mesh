import { useRef, useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import { AssigneeAvatar } from "@/components/assignee-avatar";
import { TaskCard } from "@/components/task-card";
import { MentionPickerMenu } from "@/components/mention-menu";
import { useMentionPicker } from "@/hooks/use-mention-picker";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Agent, Task } from "@/types";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getMentionables: vi.fn(), getAccessToken: () => null }));
import { api, getMentionables } from "@/lib/api";

const agent = {
  id: "agent-1", name: "Bender", short_tag: "keep-dev", role: "long unrelated role",
  workspace_id: "ws-1", agent_type: "codex", status: "offline", capabilities: {},
  metadata: {}, last_heartbeat: null, created_at: "2026-10-02", updated_at: "2026-10-02",
} as Agent;

function Composer() {
  const [value, setValue] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);
  const picker = useMentionPicker("ws-1", value, setValue, ref);
  return <><textarea aria-label="Comment" ref={ref} value={value}
    onChange={(e) => { setValue(e.target.value); picker.onValueChange(e.target.value, e.target.selectionStart); }}
    onKeyDown={picker.onKeyDown} /><MentionPickerMenu picker={picker} /></>;
}

describe("agent short tags", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.mocked(api).mockResolvedValue([]);
    useWorkspaceStore.setState({ currentWorkspace: null });
    useRulesStore.setState({ teamDirectory: null });
    useAgentStore.setState({ agents: [agent] });
  });

  it("changes only the agent avatar tooltip, preserving initials, color and human labels", () => {
    const { rerender } = render(<AssigneeAvatar name="Bender" type="agent" />);
    const original = screen.getByLabelText("Bender");
    const classes = original.className;
    rerender(<AssigneeAvatar name="Bender" type="agent" shortTag="keep-dev" />);
    expect(screen.getByLabelText("Bender · keep-dev")).toHaveTextContent("BE");
    expect(screen.getByLabelText("Bender · keep-dev").className).toBe(classes);
    rerender(<AssigneeAvatar name="Bender" type="user" shortTag="keep-dev" />);
    expect(screen.getByLabelText("Bender")).toHaveAttribute("title", "Bender");
    rerender(<AssigneeAvatar name="Bender" type="agent" shortTag="" />);
    expect(screen.getByLabelText("Bender")).toHaveAttribute("title", "Bender");
  });

  it("uses the current agent tag on a board card without adding inline text", () => {
    const task = { id: "task-1", title: "Example", priority: "none", labels: [],
      assignee_id: agent.id, assignee_type: "agent", assignee_name: agent.name } as unknown as Task;
    render(<TaskCard task={task} />);
    expect(screen.getByTitle("Bender · keep-dev")).toHaveTextContent("BE");
    expect(screen.queryByText(/keep-dev/)).toBeNull();
  });

  it.each(["mouse", "Enter"])("shows the tag but inserts only @slug with %s", async (choice) => {
    vi.mocked(getMentionables).mockResolvedValue([{ id: agent.id, kind: "agent", slug: "bender",
      display_name: "Bender", short_tag: "keep-dev", avatar_url: null }]);
    render(<Composer />);
    const input = screen.getByLabelText("Comment");
    fireEvent.change(input, { target: { value: "Hi @ben", selectionStart: 7 } });
    const option = await screen.findByRole("option");
    expect(option).toHaveTextContent("Bender · keep-dev");
    if (choice === "mouse") fireEvent.mouseDown(option);
    else fireEvent.keyDown(input, { key: "Enter" });
    expect(input).toHaveValue("Hi @bender ");
  });

  it("trims and saves the tag once on Enter followed by blur, without renaming", async () => {
    let finish!: (value: Agent) => void;
    vi.mocked(api).mockImplementation((_path, opts) => opts?.method === "PATCH"
      ? new Promise<Agent>((resolve) => { finish = resolve; }) : Promise.resolve([]));
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={agent} />);
    const input = screen.getByRole("textbox", { name: "Short tag" });
    const heading = screen.getByRole("heading", { level: 2 });
    expect(heading.querySelector("span[title]")).toHaveTextContent(/^Bender$/);
    expect(heading).not.toHaveTextContent("keep-dev");
    expect(input).toHaveValue("keep-dev");
    expect(input).toHaveAttribute("maxlength", "24");
    fireEvent.change(input, { target: { value: " mesh-dev " } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.blur(input);
    expect(vi.mocked(api).mock.calls.filter(([, o]) => o?.method === "PATCH")).toEqual([
      ["/api/v1/agents/agent-1", { method: "PATCH", body: { short_tag: "mesh-dev" } }],
    ]);
    finish({ ...agent, short_tag: "mesh-dev" });
    await waitFor(() => expect(input).toHaveValue("mesh-dev"));
  });

  it("sends null when cleared; preserves the draft and reports a rejected PATCH", async () => {
    vi.mocked(api).mockImplementation((_path, opts) => opts?.method === "PATCH"
      ? Promise.reject(new Error("Tag could not be saved")) : Promise.resolve([]));
    render(<AgentDetailDialog open onOpenChange={vi.fn()} agent={agent} />);
    const input = screen.getByRole("textbox", { name: "Short tag" });
    fireEvent.change(input, { target: { value: "" } });
    fireEvent.blur(input);
    await screen.findByText("Tag could not be saved");
    expect(input).toHaveValue("");
    expect(api).toHaveBeenCalledWith("/api/v1/agents/agent-1", { method: "PATCH", body: { short_tag: null } });
    expect(useAgentStore.getState().agents[0]!.name).toBe("Bender");
    expect(useAgentStore.getState().agents[0]!.short_tag).toBe("keep-dev");
  });
});
