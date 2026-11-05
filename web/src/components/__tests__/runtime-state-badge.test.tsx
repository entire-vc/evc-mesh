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
    expect(parseRuntimeRef("decision:@lead")).toEqual({ label: "@lead", href: null });
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
  it("compact card: brief with the known target, none of the diagnostic details", () => {
    const { container } = render(<TaskCard task={makeTask({ runtime_state: diagState })} />);
    const text = container.textContent ?? "";
    expect(screen.getByTestId("runtime-state").textContent).toBe("Waiting for task#00000000");
    expect(screen.getByTestId("runtime-state-ref").getAttribute("href")).toBe("/t/00000000");
    for (const bad of ["HTTP", "UNKNOWN", "addressee", "Wakes on", "Source", "Next", "observed"]) expect(text).not.toContain(bad);
    expect(screen.queryByTestId("runtime-constraints")).toBeNull();
  });

  it("detail: a known dependency stays visible even when the diagnostic is unreadable; raw HTTP 0 text is not", () => {
    render(<RuntimeStateBadge state={diagState} variant="line" />);
    expect(screen.getByTestId("runtime-state").textContent).toContain("Waiting for task");
    expect(screen.getByTestId("runtime-state").textContent).not.toMatch(/HTTP 0|not confirmed/);
    render(<RuntimeConstraints state={diagState} variant="full" />);
    expect(screen.getByTestId("constraint-reason").textContent).toContain("Wait not confirmed");
  });

  it("detail brief: the target is printed once; Next/Wakes/Source live in collapsed technical details; incompleteness is one short note", () => {
    const { container } = render(
      <>
        <RuntimeStateBadge state={diagState} variant="line" />
        <RuntimeConstraints state={diagState} variant="full" hideBriefTarget />
      </>,
    );
    expect(container.querySelectorAll("a[href='/t/00000000']")).toHaveLength(1);
    expect(screen.queryByTestId("runtime-constraint")).toBeNull();
    expect(screen.getByTestId("runtime-incomplete").textContent).toBe("Details incomplete");
    const details = screen.getByTestId("runtime-technical") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(details.querySelector("summary")?.textContent).toBe("Technical details");
    expect(details.textContent).not.toMatch(/HTTP 0|addressee=/);
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

describe("waiting brief: real contract shapes", () => {
  const text = () => screen.getByTestId("runtime-state").textContent;
  const link = () => screen.getByTestId("runtime-state-ref") as HTMLAnchorElement;

  it("MR ref without reason (the theme card): brief + GitLab link on the board tile", () => {
    render(<TaskCard task={makeTask({ runtime_state: { kind: "waiting", ref: "mr:entire-vc/evc-mesh!12" } })} />);
    expect(text()).toBe("Waiting for MR!12");
    expect(link().getAttribute("href")).toBe("https://gitlab.com/entire-vc/evc-mesh/-/merge_requests/12");
    expect(link().getAttribute("aria-label")).toContain("entire-vc/evc-mesh!12");
    expect(link().getAttribute("rel")).toBe("noreferrer");
  });

  it("pipeline ref: CI wording and link", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "pipeline:entire-vc/evc-mesh#9954" }} variant="kind" />);
    expect(text()).toBe("Waiting for CI#9954");
    expect(link().getAttribute("href")).toBe("https://gitlab.com/entire-vc/evc-mesh/-/pipelines/9954");
  });

  it("actual until: shown on the tile; no ref and no until stays the neutral word", () => {
    const a = render(<RuntimeStateBadge state={{ kind: "waiting", until: "2026-10-10T19:15:00Z" }} variant="kind" />);
    expect(text()).toMatch(/^Waiting until \d\d:\d\d$/);
    a.unmount();
    render(<RuntimeStateBadge state={{ kind: "waiting" }} variant="kind" />);
    expect(text()).toBe("Waiting");
    expect(screen.queryByTestId("runtime-state-ref")).toBeNull();
  });

  it("verbose WAIT prose is never promoted to a target; the tile stays neutral", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "Runner: catalog rollout archive root on SSD, then mr:g/p!3 later", reason: "x".repeat(300) }} variant="kind" />);
    expect(text()).toBe("Waiting");
    expect(screen.queryByTestId("runtime-state-ref")).toBeNull();
  });

  it("a ref on the dependency constraint only (no state ref) still names the target, even with a verbose diagnostic reason", () => {
    const st = { kind: "waiting", reason: "Blocking path diagnostic UNKNOWN (HTTP 0); addressee=Developer Three; observed_at=2026-11-04T10:48:01Z", constraints: [{ id: "dep", class: "mesh_dependency", reason_code: "dependency_unreleased", next_actor: { kind: "unknown" }, wake: { predicate: "card", ref: "card:00000000-0000-4000-8000-000000000001" }, source: { kind: "task_dependency", ref: "00000000-0000-4000-8000-000000000002" } } as OperationalConstraint] } as RuntimeState;
    const { container } = render(
      <>
        <RuntimeStateBadge state={st} variant="line" />
        <RuntimeConstraints state={st} variant="full" hideBriefTarget />
      </>,
    );
    expect(link().getAttribute("href")).toBe("/t/00000000-0000-4000-8000-000000000001");
    expect(link().textContent).toBe("#00000000");
    expect(container.querySelectorAll("a[href^='/t/']")).toHaveLength(1);
    const visible = Array.from(container.children).map((el) => (el.closest("details") ? "" : el.textContent)).join(" ");
    expect(visible).not.toMatch(/Blocking path|addressee|observed_at|HTTP 0/);
  });

  it("long short reason is kept beside the brief; long prose is dropped from the main line", () => {
    const a = render(<RuntimeStateBadge state={{ kind: "waiting", ref: "card:#00000000", reason: "needs the contract" }} variant="line" />);
    expect(text()).toContain("needs the contract");
    a.unmount();
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "card:#00000000", reason: "y".repeat(200) }} variant="line" />);
    expect(text()).not.toContain("yyyy");
  });

  it("decision ref is plain text; unsafe schemes never become links; stale tile keeps brief but no stale note", () => {
    const a = render(<RuntimeStateBadge state={{ kind: "waiting", ref: "decision:@lead" }} variant="kind" />);
    expect(text()).toBe("Waiting for decision@lead");
    expect(link().tagName).toBe("SPAN");
    a.unmount();
    const b = render(<RuntimeStateBadge state={{ kind: "waiting", ref: "javascript:alert(1)" }} variant="kind" />);
    expect(b.container.querySelector("a")).toBeNull();
    b.unmount();
    render(<RuntimeStateBadge state={{ kind: "waiting", ref: "mr:g/p!3", stale: true }} variant="kind" />);
    expect(screen.queryByTestId("runtime-state-stale")).toBeNull();
  });

  it("non-waiting kinds are unchanged on the tile (a ref on a working card is not a wait target)", () => {
    render(<RuntimeStateBadge state={{ kind: "working", ref: "mr:g/p!3", lane: "Developer Two" }} variant="kind" />);
    expect(text()).toBe("Working");
    expect(screen.queryByTestId("runtime-state-ref")).toBeNull();
  });

  it("a redacted constraint never supplies the target, even when it comes first and carries a ref", () => {
    const st = { kind: "waiting", constraints: [c({ id: "r", class: "write_acl", reason_code: "access_denied", wake: { predicate: "card", ref: "card:#deadbeef" } })] } as RuntimeState;
    const { container } = render(<RuntimeStateBadge state={st} variant="kind" />);
    expect(text()).toBe("Waiting");
    expect(container.textContent).not.toContain("deadbeef");
  });

  it("only one plain constraint repeats the brief; a stale or CI one with the same ref keeps its row", () => {
    const ref = { predicate: "card", ref: "card:#00000000" };
    const st = waiting([
      c({ id: "p", class: "mesh_dependency", reason_code: "dependency_unreleased", wake: ref }),
      c({ id: "s", class: "local_wait", stale: true, wake: ref }),
      c({ id: "ci", class: "ci_capacity", ci: { state: "manual_required" }, wake: ref }),
    ]);
    render(<RuntimeConstraints state={st} variant="full" hideBriefTarget />);
    const rows = screen.getAllByTestId("runtime-constraint").map((li) => li.getAttribute("data-constraint-key"));
    expect(rows).toEqual(["s", "ci"]);
    expect(screen.queryByTestId("constraint-ref")).toBeNull(); // the rows keep their facts, the target is named by the brief
    expect(screen.getAllByTestId("runtime-technical-item")).toHaveLength(3);
  });

  it("detail without a target: long reason prose is not in the Now line, it is in the collapsed technical details", () => {
    const prose = "Runner: catalog rollout archive root on SSD, waiting for the install window and the lane to free up, then retry";
    const st = { kind: "waiting", reason: prose } as RuntimeState;
    const { container } = render(
      <>
        <RuntimeStateBadge state={st} variant="line" />
        <RuntimeConstraints state={st} variant="full" hideBriefTarget />
      </>,
    );
    expect(text()).toBe("Waiting");
    const tech = screen.getByTestId("runtime-technical") as HTMLDetailsElement;
    expect(tech.open).toBe(false);
    expect(screen.getByTestId("runtime-technical-reason").textContent).toBe(prose);
    expect(container.querySelectorAll("details")).toHaveLength(1);
  });

  it("detail without a target: a short reason stays beside Waiting; a diagnostic reason never reaches the technical details", () => {
    const a = render(<RuntimeStateBadge state={{ kind: "waiting", reason: "human gate" }} variant="line" />);
    expect(text()).toContain("human gate");
    a.unmount();
    const diag = { kind: "waiting", reason: "Blocking path diagnostic UNKNOWN (HTTP 0); addressee=Developer Three" } as RuntimeState;
    const b = render(<RuntimeConstraints state={diag} variant="full" hideBriefTarget />);
    expect(b.container.innerHTML).toBe("");
  });

  it("technical details show a long reason in full (beyond the 240-char display cap)", () => {
    const prose = `Runner: ${"waiting for the install window; ".repeat(15)}done`;
    expect(prose.length).toBeGreaterThan(400);
    render(<RuntimeConstraints state={{ kind: "waiting", reason: prose } as RuntimeState} variant="full" hideBriefTarget />);
    expect(screen.getByTestId("runtime-technical-reason").textContent).toBe(prose);
  });

  it("technical details: a long constraint reason is shown in full while the row stays capped; control chars never render", () => {
    const long = `free text ${"x".repeat(500)} END`;
    const st = waiting([c({ id: "L", class: "brand_new", reason_code: "brand_new_code", reason: long })]);
    render(<RuntimeConstraints state={st} variant="full" />);
    expect(screen.getByTestId("constraint-reason").textContent!.length).toBeLessThan(long.length);
    expect(screen.getByTestId("runtime-technical-item").querySelector("dd")?.textContent).toBe(long);
  });

  it("a reason parameter with control characters is cleaned in the Now line", () => {
    render(<RuntimeStateBadge state={{ kind: "waiting", reason_code: "review_cooldown", reason_params: { minutes: "4\u0007\u001b[31m" } } as RuntimeState} variant="line" />);
    // eslint-disable-next-line no-control-regex
    expect(text()).not.toMatch(/[\u0000-\u001f\u007f]/);
  });

  it("a recognised constraint reason code with control characters in reason_params is cleaned in the row and in technical details", () => {
    const st = waiting([c({ id: "P", class: "local_wait", reason_code: "review_cooldown", reason_params: { minutes: "4\u0007\u001b[31m" } })]);
    const { container } = render(<RuntimeConstraints state={st} variant="full" />);
    // eslint-disable-next-line no-control-regex
    expect(container.textContent).not.toMatch(/[\u0000-\u001f\u007f]/);
  });

  it("a ref that is not a recognised target never reaches the detail line raw; the dense badge sanitises it", () => {
    const raw = "Blocking path diagnostic\u0007 UNKNOWN (HTTP 0) addressee=Developer Three";
    const a = render(<RuntimeStateBadge state={{ kind: "waiting", ref: raw }} variant="line" />);
    expect(screen.queryByTestId("runtime-state-ref")).toBeNull();
    expect(a.container.textContent).not.toMatch(/HTTP 0|addressee/);
    a.unmount();
    const b = render(<RuntimeStateBadge state={{ kind: "waiting", ref: "free text\u0007 ref" }} />);
    // eslint-disable-next-line no-control-regex
    expect(b.container.textContent).not.toMatch(/[\u0000-\u001f\u007f]/);
  });

  it("a dependency the caller cannot open never supplies a target or a link, even when the payload carries a ref", () => {
    const st = waiting([c({ id: "h", class: "mesh_dependency", reason_code: "dependency_not_visible", wake: { predicate: "card", ref: "card:deadbeef-1b2c-4d3e-8f90-a1b2c3d4e5f6" } })]);
    const a = render(<RuntimeStateBadge state={st} variant="kind" />);
    expect(text()).toBe("Waiting");
    expect(a.container.textContent).not.toContain("deadbeef");
    a.unmount();
    const { container } = render(<RuntimeConstraints state={st} variant="full" hideBriefTarget />);
    expect(screen.queryByTestId("constraint-ref")).toBeNull();
    expect(container.querySelector("a")).toBeNull();
    expect(screen.getByTestId("constraint-reason").textContent).toBe("Blocked by a card you cannot open");
  });

  it("a state-level ref that points at a hidden dependency is not a target either", () => {
    const hid = c({ id: "h", class: "mesh_dependency", reason_code: "dependency_not_visible", wake: { predicate: "card", ref: "card:deadbeef-1b2c-4d3e-8f90-a1b2c3d4e5f6" } });
    const st = { kind: "waiting", ref: "card:#deadbeef", constraints: [hid] } as RuntimeState;
    const a = render(<RuntimeStateBadge state={st} variant="kind" />);
    expect(text()).toBe("Waiting");
    expect(a.container.textContent).not.toContain("deadbeef");
    a.unmount();
    const b = render(<RuntimeStateBadge state={st} variant="line" />);
    expect(b.container.querySelector("a")).toBeNull();
    b.unmount();
    // a different, visible state-level target is still shown
    render(<RuntimeStateBadge state={{ ...st, ref: "mr:g/p!3" } as RuntimeState} variant="kind" />);
    expect(text()).toBe("Waiting for MR!3");
  });

  it("working / queued / stopped detail text is cleaned of control characters (lane and reason)", () => {
    for (const st of [
      { kind: "working", lane: "Ca\u0007rl" },
      { kind: "queued", lane: "Ca\u0007rl", position: 1 },
      { kind: "stopped", reason: "refeed\u001b[31m-cap" },
    ] as RuntimeState[]) {
      const { container, unmount } = render(<RuntimeStateBadge state={st} variant="line" />);
      // eslint-disable-next-line no-control-regex
      expect(container.textContent).not.toMatch(/[\u0000-\u001f\u007f]/);
      unmount();
    }
  });

  it("hidden-target matching ignores id casing (a case-variant state ref is still the hidden card)", () => {
    const hid = c({ id: "h", class: "mesh_dependency", reason_code: "dependency_not_visible", wake: { predicate: "card", ref: "card:deadbeef-1b2c-4d3e-8f90-a1b2c3d4e5f6" } });
    const { container } = render(<RuntimeStateBadge state={{ kind: "waiting", ref: "card:#DEADBEEF", constraints: [hid] } as RuntimeState} variant="kind" />);
    expect(text()).toBe("Waiting");
    expect(container.textContent?.toLowerCase()).not.toContain("deadbeef");
    expect(container.querySelector("a")).toBeNull();
  });

  it("detail names the target once even when the state ref and the constraint ref differ only in id casing", () => {
    const st = { kind: "waiting", ref: "card:#DEADBEEF", constraints: [c({ id: "d", class: "mesh_dependency", reason_code: "dependency_unreleased", wake: { predicate: "card", ref: "card:#deadbeef" } })] } as RuntimeState;
    const { container } = render(
      <>
        <RuntimeStateBadge state={st} variant="line" />
        <RuntimeConstraints state={st} variant="full" hideBriefTarget />
      </>,
    );
    expect(container.querySelectorAll("a")).toHaveLength(1);
    expect(screen.queryByTestId("runtime-constraint")).toBeNull();
  });

  it("a stale or CI constraint that repeats the brief target keeps its facts but not the link", () => {
    const st = { kind: "waiting", ref: "card:#00000000", constraints: [c({ id: "ci", class: "ci_capacity", ci: { state: "manual_required" }, wake: { predicate: "card", ref: "card:#00000000" } })] } as RuntimeState;
    const { container } = render(
      <>
        <RuntimeStateBadge state={st} variant="line" />
        <RuntimeConstraints state={st} variant="full" hideBriefTarget />
      </>,
    );
    expect(container.querySelectorAll("a[href='/t/00000000']")).toHaveLength(1);
    expect(screen.getByTestId("constraint-ci").textContent).toBe("Manual action required");
  });

  it("dependency_not_visible leaks no metadata (source, wake, actor, time) in the technical details", () => {
    const st = waiting([c({ id: "h", class: "mesh_dependency", reason_code: "dependency_not_visible", next_actor: { name: "secret-actor" }, observed_at: "2026-11-04T10:00:00Z", source: "gitlab:secret/path!9", wake: { predicate: "card", ref: "card:deadbeef-1b2c-4d3e-8f90-a1b2c3d4e5f6" } } as never)]);
    const { container } = render(<RuntimeConstraints state={st} variant="full" hideBriefTarget />);
    const text = container.textContent ?? "";
    for (const bad of ["deadbeef", "secret", "gitlab:"]) expect(text).not.toContain(bad);
    expect(container.querySelector("a")).toBeNull();
  });

  it("technical details carry the raw target ref while the main detail names it once", () => {
    const st = waiting([c({ id: "d", class: "mesh_dependency", wake: { predicate: "card", ref: "card:#00000000" } })]);
    const { container } = render(<RuntimeConstraints state={st} variant="full" />);
    expect(screen.getByTestId("constraint-target").textContent).toBe("card:#00000000");
    expect(container.querySelector("details")?.contains(screen.getByTestId("constraint-target"))).toBe(true);
  });

  it("a state-level target without constraints still has its raw ref in collapsed technical details", () => {
    const uuid = "card:00000000-1b2c-4d3e-8f90-a1b2c3d4e5f6";
    const st = { kind: "waiting", ref: uuid } as RuntimeState;
    const { container } = render(<RuntimeConstraints state={st} variant="full" hideBriefTarget />);
    const d = container.querySelector("details");
    expect(d).not.toBeNull();
    expect(d?.querySelector('[data-testid="runtime-technical-target"]')?.textContent).toBe(uuid);
  });
});
