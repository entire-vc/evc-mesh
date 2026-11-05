import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TaskCard } from "@/components/task-card";
import { RuntimeStateBadge } from "@/components/runtime-state-badge";
import { RuntimeConstraints } from "@/components/runtime-constraints";
import { isWorking, parseRuntimeRef } from "@/lib/runtime-state";
import type { OperationalConstraint, RuntimeState, Task } from "@/types";

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: "task-1",
    project_id: "proj-1",
    status_id: "status-1",
    title: "A card",
    assignee_id: "aaaaaaaa-1111-1111-1111-111111111111",
    assignee_type: "agent",
    assignee_name: "Developer Two",
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
    render(<RuntimeStateBadge state={{ kind: "working", lane: "Developer Two", since: "2026-10-10T07:20:00Z" }} />);
    expect(kind()).toBe("working");
    expect(screen.getByTestId("runtime-state").textContent).toMatch(/Being worked on by Developer Two since \d\d:\d\d/);
  });

  it("waiting: renders a clickable card ref", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "card:#00000000" }} />);
    expect(kind()).toBe("waiting");
    const link = screen.getByTestId("runtime-state-ref");
    expect(link.textContent).toBe("#00000000");
    expect(link.getAttribute("href")).toBe("/t/00000000");
  });

  it("waiting: mr and pipeline refs link to GitLab, decision stays plain text", () => {
    expect(parseRuntimeRef("mr:entire-vc/evc-mesh!12")?.href).toBe("https://gitlab.com/entire-vc/evc-mesh/-/merge_requests/12");
    expect(parseRuntimeRef("pipeline:entire-vc/evc-mesh#9954")?.href).toBe("https://gitlab.com/entire-vc/evc-mesh/-/pipelines/9954");
    expect(parseRuntimeRef("decision:@garfield")).toEqual({ label: "@garfield", href: null });
    expect(parseRuntimeRef(null)).toBeNull();
  });

  it("waiting on a time: shows until", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", until: "2026-10-10T19:15:00Z", reason: "event in the log" }} />);
    expect(screen.getByTestId("runtime-state").textContent).toMatch(/Waiting: event in the log until \d\d:\d\d/);
  });

  it("queued: lane and position", () => {
    render(<RuntimeStateBadge state={{ kind: "queued", lane: "Developer Two", position: 2 }} />);
    expect(kind()).toBe("queued");
    expect(screen.getByTestId("runtime-state").textContent).toContain("Queued in lane Developer Two, position 2");
  });

  it("stopped: shows the reason", () => {
    render(<RuntimeStateBadge state={{ kind: "stopped", reason: "refeed-cap" }} />);
    expect(kind()).toBe("stopped");
    expect(screen.getByTestId("runtime-state").textContent).toContain("Stopped: refeed-cap");
  });

  it("stale: muted with an explicit note, kind is kept", () => {
    render(<RuntimeStateBadge state={{ kind: "working", lane: "Developer Two", stale: true }} />);
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
    expect(screen.getByTestId("runtime-state").textContent).toBe("Working");
    expect(a.container.textContent).not.toContain(lane);
    a.unmount();
    const b = render(<TaskCard task={makeTask({ runtime_state: rs, assignee_id: "bbbbbbbb-2222-2222-2222-222222222222" })} />);
    expect(b.container.textContent).not.toContain(lane);
  });
});

function c(over: Partial<OperationalConstraint> & { id: string; class: string }): OperationalConstraint {
  return {
    reason_code: "x",
    next_actor: { kind: "unknown" },
    wake: { predicate: "unknown" },
    source: { kind: "mesh" },
    observed_at: "2026-11-03T10:00:00Z",
    stale: false,
    ...over,
  } as OperationalConstraint;
}

const classes = ["mesh_dependency", "local_wait", "ci_capacity", "review_rejection", "expired_authority", "write_acl"];
const waiting = (constraints: OperationalConstraint[]): RuntimeState => ({ kind: "waiting", constraints });

describe("RuntimeConstraints", () => {
  it.each(classes)("class %s: localized label, reason and ref together", (k) => {
    render(
      <RuntimeConstraints
        variant="full"
        state={waiting([c({ id: k, class: k, reason_code: k === "mesh_dependency" ? "mesh_dependency" : "local_wait", wake: { predicate: "card:#00000000", ref: "card:#00000000" }, next_actor: { kind: "agent", name: "Developer One" } })])}
      />,
    );
    const li = screen.getByTestId("runtime-constraint");
    expect(li.getAttribute("data-constraint-class")).toBe(k);
    expect(screen.getByTestId("constraint-class").textContent).not.toMatch(/^(Constraint)$/);
    expect(screen.getByTestId("constraint-reason").textContent).not.toBe("");
    expect(screen.getByTestId("constraint-ref").getAttribute("href")).toBe("/t/00000000");
    expect(screen.getByTestId("constraint-next-actor").textContent).toBe("Developer One");
    expect(screen.getByTestId("constraint-wake").textContent).toBe("card:#00000000");
  });

  it("dependency and a distinct local WAIT are both visible in detail", () => {
    const st = waiting([
      c({ id: "d", class: "mesh_dependency", reason_code: "mesh_dependency", wake: { predicate: "card:#aaaaaaaa", ref: "card:#aaaaaaaa" } }),
      c({ id: "w", class: "local_wait", reason_code: "local_wait", wake: { predicate: "mr:g/p!3", ref: "mr:g/p!3" } }),
    ]);
    render(<RuntimeConstraints state={st} variant="full" />);
    expect(screen.getAllByTestId("runtime-constraint")).toHaveLength(2);
  });

  it("detail shows every constraint (no cap)", () => {
    render(<RuntimeConstraints state={waiting([1, 2, 3, 4].map((i) => c({ id: `i${i}`, class: "local_wait" })))} variant="full" />);
    expect(screen.getAllByTestId("runtime-constraint")).toHaveLength(4);
  });

  it("reason is not hidden by ref in the badge text (regression: runtime-state.ts hid it)", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "card:#00000000", reason: "needs the contract" }} />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("needs the contract");
    expect(screen.getByTestId("runtime-state-ref").textContent).toBe("#00000000");
  });

  it("unknown actor/predicate render as unknown, nothing invented; unknown class/code fall back to free text", () => {
    render(<RuntimeConstraints variant="full" state={waiting([c({ id: "u", class: "brand_new", reason_code: "brand_new_code", reason: "free text" })])} />);
    expect(screen.getByTestId("constraint-reason").textContent).toBe("free text");
    expect(screen.getByTestId("constraint-next-actor").textContent).toBe("unknown");
    expect(screen.getByTestId("constraint-wake").textContent).toBe("unknown");
  });

  it("403/redacted constraint shows a restricted note, no ref, no owner", () => {
    render(<RuntimeConstraints variant="full" state={waiting([c({ id: "r", class: "write_acl", reason_code: "access_denied" })])} />);
    expect(screen.getByTestId("constraint-reason").textContent).toBe("Details are restricted");
    expect(screen.queryByTestId("constraint-ref")).toBeNull();
    expect(screen.getByTestId("constraint-next-actor").textContent).toBe("unknown");
  });

  it("redacted constraint hides every other field even when the payload carries them", () => {
    render(
      <RuntimeConstraints
        variant="full"
        state={waiting([
          c({
            id: "r2",
            class: "write_acl",
            reason_code: "access_denied",
            next_actor: { kind: "agent", name: "SecretOwner" },
            wake: { predicate: "mr:secret/p!1", ref: "mr:secret/p!1" },
            source: { kind: "mesh", ref: "secret-source" },
            ci: { state: "capacity", scope: "secret-scope", position: 1 },
          }),
        ])}
      />,
    );
    const text = screen.getByTestId("runtime-constraint").textContent ?? "";
    expect(text).not.toMatch(/SecretOwner|secret/);
    expect(screen.queryByTestId("constraint-ref")).toBeNull();
    expect(screen.queryByTestId("constraint-ci")).toBeNull();
  });

  it("stale constraint is muted and labelled", () => {
    render(<RuntimeConstraints state={waiting([c({ id: "s", class: "local_wait", stale: true })])} />);
    expect(screen.getByTestId("runtime-constraint").getAttribute("data-constraint-stale")).toBe("true");
    expect(screen.getByTestId("constraint-stale").textContent).toContain("stale");
  });

  it("CI: states are distinguished, unavailable never shows scope/position", () => {
    const a = render(<RuntimeConstraints state={waiting([c({ id: "1", class: "ci_capacity", ci: { state: "capacity", scope: "runner-pool-a", position: 3 } })])} />);
    expect(screen.getByTestId("constraint-ci").textContent).toBe("Waiting for CI capacity, scope runner-pool-a, position 3");
    a.unmount();
    const b = render(<RuntimeConstraints state={waiting([c({ id: "2", class: "ci_capacity", ci: { state: "manual_required" } })])} />);
    expect(screen.getByTestId("constraint-ci").textContent).toBe("Manual action required");
    b.unmount();
    render(<RuntimeConstraints state={waiting([c({ id: "3", class: "ci_capacity", ci: { state: "unavailable", scope: "invented", position: 9 } })])} />);
    expect(screen.getByTestId("constraint-ci").textContent).toBe("CI status unavailable");
  });

  it("lane position stays the lane's: a queued state does not become a CI position", () => {
    render(<RuntimeStateBadge state={{ kind: "queued", lane: "Developer Two", position: 2, constraints: [c({ id: "q", class: "ci_capacity", ci: { state: "unavailable" } })] }} />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("position 2");
    render(<RuntimeConstraints state={{ kind: "queued", position: 2, constraints: [c({ id: "q", class: "ci_capacity", ci: { state: "unavailable" } })] }} />);
    expect(screen.getByTestId("constraint-ci").textContent).not.toContain("position");
  });

  it("unsafe refs never become links; long refs are plain text", () => {
    render(<RuntimeConstraints state={waiting([c({ id: "x", class: "local_wait", wake: { predicate: "p", ref: "javascript:alert(1)" } })])} />);
    expect(screen.getByTestId("constraint-ref").tagName).toBe("SPAN");
    expect(screen.getByTestId("constraint-ref").getAttribute("href")).toBeNull();
  });

  it("malformed refs (recognised prefix + junk) stay plain text", () => {
    const junk = "card:#" + "0123" + "abcd extra";
    render(<RuntimeConstraints state={waiting([c({ id: "m", class: "local_wait", wake: { predicate: "p", ref: junk } })])} />);
    expect(screen.getByTestId("constraint-ref").tagName).toBe("SPAN");
  });

  it("server dependency constraint: full-UUID card ref links to the card, code text is translated", () => {
    const id = "00000000-1b2c-4d3e-8f90-a1b2c3d4e5f6";
    render(<RuntimeConstraints variant="full" state={waiting([c({ id: "dep:1", class: "mesh_dependency", reason_code: "dependency_unreleased", wake: { predicate: "card", ref: `card:${id}` }, source: { kind: "task_dependency", ref: "e1" } })])} />);
    expect(screen.getByTestId("constraint-reason").textContent).toBe("Blocked by another card");
    expect(screen.getByTestId("constraint-ref").textContent).toBe("#00000000");
    expect(screen.getByTestId("constraint-ref").getAttribute("href")).toBe(`/t/${id}`);
  });

  it("server dependency constraint the caller cannot open: no ref, own reason text, no leak", () => {
    render(<RuntimeConstraints variant="full" state={waiting([c({ id: "dep:2", class: "mesh_dependency", reason_code: "dependency_not_visible", wake: { predicate: "unknown" } })])} />);
    expect(screen.getByTestId("constraint-reason").textContent).toBe("Blocked by a card you cannot open");
    expect(screen.queryByTestId("constraint-ref")).toBeNull();
  });

  it("negative control: a full-UUID ref with a malformed tail stays plain text", () => {
    render(<RuntimeConstraints state={waiting([c({ id: "u", class: "local_wait", wake: { predicate: "p", ref: "card:00000000-1b2c-4d3e-8f90-a1b2c3d4e5f6/../x" } })])} />);
    expect(screen.getByTestId("constraint-ref").tagName).toBe("SPAN");
  });

  it("unknown CI state shows no scope/position", () => {
    render(<RuntimeConstraints state={waiting([c({ id: "k", class: "ci_capacity", ci: { state: "brand_new", scope: "s", position: 4 } })])} />);
    expect(screen.getByTestId("constraint-ci").textContent).toBe("CI status unavailable");
  });

  it("legacy: no constraints renders nothing, empty array renders nothing", () => {
    const a = render(<RuntimeConstraints state={{ kind: "waiting", ref: "card:#00000000" }} />);
    expect(a.container.innerHTML).toBe("");
    a.unmount();
    const b = render(<RuntimeConstraints state={waiting([])} />);
    expect(b.container.innerHTML).toBe("");
  });
});

describe("constraints=[] keeps legacy state visible", () => {
  it("empty array does not hide legacy reason/ref in the badge", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "card:#00000000", reason: "human gate", constraints: [] }} />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("human gate");
    expect(screen.getByTestId("runtime-state-ref").textContent).toBe("#00000000");
  });
});

const DIAG = "Blocking path diagnostic UNKNOWN (HTTP 0); addressee=unknown; observed_at=2026-11-04T10:48:01.712+00:00 #00000000";
const diagState: RuntimeState = {
  kind: "waiting",
  reason: DIAG,
  ref: "card:#00000000",
  constraints: [
    { id: "k1", class: "mesh_dependency", reason: DIAG, reason_code: "x", next_actor: { kind: "unknown" }, wake: { predicate: "card", ref: "card:#00000000" }, source: { kind: "task_dependency", ref: "00000000" } } as OperationalConstraint,
  ],
} as RuntimeState;

describe("kanban card hides service diagnostics; detail marks them unconfirmed", () => {
  it("compact card: kind word only, none of the diagnostic details", () => {
    const { container } = render(<TaskCard task={makeTask({ runtime_state: diagState })} />);
    const text = container.textContent ?? "";
    expect(screen.getByTestId("runtime-state").textContent).toBe("Waiting");
    for (const bad of ["HTTP", "UNKNOWN", "addressee", "Wakes on", "Source", "Next", "observed", "#00000000"]) expect(text).not.toContain(bad);
    expect(screen.queryByTestId("runtime-constraints")).toBeNull();
  });

  it("detail: unreadable diagnostic is shown as unconfirmed, raw HTTP 0 text is not", () => {
    render(<RuntimeStateBadge state={diagState} variant="line" />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("Wait not confirmed");
    expect(screen.getByTestId("runtime-state").textContent).not.toContain("HTTP 0");
    render(<RuntimeConstraints state={diagState} variant="full" />);
    expect(screen.getByTestId("constraint-reason").textContent).toContain("Wait not confirmed");
  });

  it("detail: a confirmed wait keeps its reason", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", reason: "Review pending", ref: "card:#00000000" }} variant="line" />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("Review pending");
    expect(screen.getByTestId("runtime-state").textContent).not.toContain("not confirmed");
  });

  it("detail: unreadable CI diagnostic never shows raw text, in reason or CI details", () => {
    const st = { kind: "waiting", constraints: [{ id: "ci1", class: "ci_capacity", reason: "CI probe UNKNOWN (HTTP 0)", reason_code: "x", next_actor: { kind: "unknown" }, wake: { predicate: "unknown" }, ci: { state: "unavailable", reason: "HTTP 0 from provider" } } as OperationalConstraint] } as RuntimeState;
    const { container } = render(<RuntimeConstraints state={st} variant="full" />);
    expect(screen.getByTestId("constraint-reason").textContent).toContain("Wait not confirmed");
    expect(container.textContent).not.toMatch(/HTTP 0|UNKNOWN/);
  });

  it("detail: a diagnostic only in ci.reason also marks the wait unconfirmed, on badge and constraint", () => {
    const st = { kind: "waiting", reason: "Waiting for CI", constraints: [{ id: "ci2", class: "ci_capacity", reason: "Waiting for CI", reason_code: "x", next_actor: { kind: "unknown" }, wake: { predicate: "unknown" }, ci: { state: "capacity", reason: "provider UNKNOWN (HTTP 0)" } } as OperationalConstraint] } as RuntimeState;
    const a = render(<RuntimeStateBadge state={st} variant="line" />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("Wait not confirmed");
    a.unmount();
    const { container } = render(<RuntimeConstraints state={st} variant="full" />);
    expect(screen.getByTestId("constraint-reason").textContent).toContain("Wait not confirmed");
    expect(container.textContent).not.toMatch(/HTTP 0|UNKNOWN/);
  });

  it("detail: a redacted (access_denied) constraint also never shows a raw diagnostic", () => {
    const st = { kind: "waiting", constraints: [{ id: "r1", class: "write_acl", reason_code: "access_denied", reason: "probe UNKNOWN (HTTP 0)", next_actor: { kind: "unknown" }, wake: { predicate: "unknown" } } as OperationalConstraint] } as RuntimeState;
    const { container } = render(<RuntimeConstraints state={st} variant="full" />);
    expect(container.textContent).not.toMatch(/HTTP 0|UNKNOWN/);
  });

  it("detail: a diagnostic in ANY displayed constraint field (scope, source, actor, wake) is hidden and marks the wait unconfirmed", () => {
    const base = { id: "f", class: "ci_capacity", reason: "Waiting for CI", reason_code: "x", next_actor: { kind: "unknown" }, wake: { predicate: "unknown" } };
    const variants: OperationalConstraint[] = [
      { ...base, ci: { state: "capacity", scope: "HTTP 0" } },
      { ...base, source: { kind: "probe", ref: "UNKNOWN (HTTP 0)" } },
      { ...base, next_actor: { kind: "agent", name: "addressee=unknown" } },
      { ...base, wake: { predicate: "HTTP 0" } },
    ] as OperationalConstraint[];
    for (const v of variants) {
      const st = { kind: "waiting", constraints: [v] } as RuntimeState;
      const a = render(<RuntimeStateBadge state={st} variant="line" />);
      expect(screen.getByTestId("runtime-state").textContent).toContain("Wait not confirmed");
      a.unmount();
      const { container, unmount } = render(<RuntimeConstraints state={st} variant="full" />);
      expect(container.textContent, JSON.stringify(v)).not.toMatch(/HTTP 0|UNKNOWN|addressee=/);
      unmount();
    }
  });

  it("detail: the literal predicate/actor 'unknown' alone is not a diagnostic (a normal wait stays confirmed)", () => {
    const st = { kind: "waiting", reason: "Review pending", constraints: [{ id: "n", class: "local_wait", reason: "Review pending", reason_code: "x", next_actor: { kind: "unknown" }, wake: { predicate: "unknown" } } as OperationalConstraint] } as RuntimeState;
    render(<RuntimeStateBadge state={st} variant="line" />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("Review pending");
  });

  it("detail: a bare 'UNKNOWN' (any case) in reason / ci.reason / source is a diagnostic; only the literal wake predicate 'unknown' is not", () => {
    const base = { id: "u", class: "local_wait", reason_code: "x", next_actor: { kind: "unknown" }, wake: { predicate: "unknown" } };
    for (const v of [{ ...base, reason: "UNKNOWN" }, { ...base, reason: "ok", ci: { state: "capacity", reason: "Unknown" } }, { ...base, reason: "ok", source: { kind: "unknown" } }] as OperationalConstraint[]) {
      const st = { kind: "waiting", constraints: [v] } as RuntimeState;
      const a = render(<RuntimeStateBadge state={st} variant="line" />);
      expect(screen.getByTestId("runtime-state").textContent, JSON.stringify(v)).toContain("Wait not confirmed");
      a.unmount();
    }
  });

  it("diagnostic inside reason_params marks the wait unconfirmed (top-level and constraint)", () => {
    const top = { kind: "waiting", reason_code: "review_refeed_hold", reason_params: { minutes: "HTTP 0" } } as RuntimeState;
    const a = render(<RuntimeStateBadge state={top} variant="line" />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("Wait not confirmed");
    a.unmount();
    const st = { kind: "waiting", constraints: [{ id: "p", class: "local_wait", reason_code: "review_refeed_hold", reason_params: { minutes: "UNKNOWN" }, next_actor: { kind: "unknown" }, wake: { predicate: "unknown" } } as unknown as OperationalConstraint] } as RuntimeState;
    const { container } = render(<RuntimeConstraints state={st} variant="full" />);
    expect(screen.getByTestId("constraint-reason").textContent).toContain("Wait not confirmed");
    expect(container.textContent).not.toMatch(/HTTP 0|UNKNOWN/);
  });

  it("compact kind badge: stale state adds nothing beyond the kind word, including the tooltip", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", stale: true, reason: "Review pending" } as RuntimeState} variant="kind" />);
    const el = screen.getByTestId("runtime-state");
    expect(el.textContent).toBe("Waiting");
    expect(el.getAttribute("title")).toBe("Waiting");
  });
});
