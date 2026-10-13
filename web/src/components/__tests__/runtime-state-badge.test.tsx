import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TaskCard } from "@/components/task-card";
import { RuntimeStateBadge } from "@/components/runtime-state-badge";
import { isWorking, parseRuntimeRef } from "@/lib/runtime-state";
import type { RuntimeState, Task } from "@/types";

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: "task-1",
    project_id: "proj-1",
    status_id: "status-1",
    title: "A card",
    assignee_id: "aaaaaaaa-1111-1111-1111-111111111111",
    assignee_type: "agent",
    assignee_name: "Carl",
    priority: "medium",
    parent_task_id: null,
    position: 0,
    due_date: null,
    estimated_hours: null,
    custom_fields: null,
    labels: null,
    created_by: "cccccccc-3333-3333-3333-333333333333",
    created_by_type: "agent",
    created_at: "2026-10-10T10:00:00Z",
    updated_at: "2026-10-10T10:00:00Z",
    completed_at: null,
    human_gate: false,
    ...overrides,
  } as Task;
}

const kind = () => screen.getByTestId("runtime-state").getAttribute("data-runtime-kind");

describe("RuntimeStateBadge", () => {
  it("working: names the lane and start time", () => {
    render(<RuntimeStateBadge state={{ kind: "working", lane: "Carl", since: "2026-10-10T07:20:00Z" }} />);
    expect(kind()).toBe("working");
    expect(screen.getByTestId("runtime-state").textContent).toMatch(/Being worked on by Carl since \d\d:\d\d/);
  });

  it("waiting: renders a clickable card ref", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "card:#467887a7" }} />);
    expect(kind()).toBe("waiting");
    const link = screen.getByTestId("runtime-state-ref");
    expect(link.textContent).toBe("#467887a7");
    expect(link.getAttribute("href")).toBe("/t/467887a7");
  });

  it("waiting: mr and pipeline refs link to GitLab, decision stays plain text", () => {
    expect(parseRuntimeRef("mr:entire-vc/evc-mesh!12")?.href).toBe("https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/12");
    expect(parseRuntimeRef("pipeline:entire-vc/evc-mesh#9954")?.href).toBe("https://git.entire.host/entire-vc/evc-mesh/-/pipelines/9954");
    expect(parseRuntimeRef("decision:@garfield")).toEqual({ label: "@garfield", href: null });
    expect(parseRuntimeRef(null)).toBeNull();
  });

  it("waiting on a time: shows until", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", until: "2026-10-10T19:15:00Z", reason: "event in the log" }} />);
    expect(screen.getByTestId("runtime-state").textContent).toMatch(/Waiting: event in the log until \d\d:\d\d/);
  });

  it("queued: lane and position", () => {
    render(<RuntimeStateBadge state={{ kind: "queued", lane: "Carl", position: 2 }} />);
    expect(kind()).toBe("queued");
    expect(screen.getByTestId("runtime-state").textContent).toContain("Queued in lane Carl, position 2");
  });

  it("stopped: shows the reason", () => {
    render(<RuntimeStateBadge state={{ kind: "stopped", reason: "refeed-cap" }} />);
    expect(kind()).toBe("stopped");
    expect(screen.getByTestId("runtime-state").textContent).toContain("Stopped: refeed-cap");
  });

  it("stale: muted with an explicit note, kind is kept", () => {
    render(<RuntimeStateBadge state={{ kind: "working", lane: "Carl", stale: true }} />);
    const el = screen.getByTestId("runtime-state");
    expect(el.getAttribute("data-runtime-stale")).toBe("true");
    expect(el.className).toContain("opacity-60");
    expect(screen.getByTestId("runtime-state-stale").textContent).toContain("data is stale");
    expect(el.getAttribute("title")).toContain("data is stale");
  });

  it("fresh value has no stale note", () => {
    render(<RuntimeStateBadge state={{ kind: "working" }} />);
    expect(screen.queryByTestId("runtime-state-stale")).toBeNull();
  });
});

describe("TaskCard runtime_state", () => {
  it("renders nothing when the task has no runtime_state", () => {
    render(<TaskCard task={makeTask()} />);
    expect(screen.queryByTestId("runtime-state")).toBeNull();
  });

  it.each(["working", "waiting", "queued", "stopped"] as const)("renders %s on the board card", (k) => {
    const rs: RuntimeState = { kind: k };
    render(<TaskCard task={makeTask({ runtime_state: rs })} />);
    expect(kind()).toBe(k);
  });

  it("isWorking only matches kind=working (todo card with work counts, waiting does not)", () => {
    expect(isWorking(makeTask({ runtime_state: { kind: "working" } }))).toBe(true);
    expect(isWorking(makeTask({ runtime_state: { kind: "waiting" } }))).toBe(false);
    expect(isWorking(makeTask())).toBe(false);
  });

  it("checkout fallback names the lane by agent id: shows the assignee name, never a raw uuid", () => {
    const lane = "aaaaaaaa-1111-1111-1111-111111111111";
    const rs: RuntimeState = { kind: "working", since: "2026-10-10T10:00:00Z", lane, source: "checkout", stale: false } as RuntimeState;
    const a = render(<TaskCard task={makeTask({ runtime_state: rs })} />);
    expect(screen.getByTestId("runtime-state").textContent).toMatch(/Being worked on by Carl/);
    expect(a.container.textContent).not.toContain(lane);
    a.unmount();
    const b = render(<TaskCard task={makeTask({ runtime_state: rs, assignee_id: "bbbbbbbb-2222-2222-2222-222222222222" })} />);
    expect(b.container.textContent).not.toContain(lane);
  });
});
