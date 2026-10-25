// Red controls of the SDK contract suites (card T6.2): a view that breaks the contract must fail the matching tests,
// and the same view once fixed must pass. A suite that cannot go red proves nothing.
import { appendFileSync } from "node:fs";
import { useEffect, type ComponentType } from "react";
import { describe, expect, it } from "vitest";
import { cleanupMounts, mountSlot, runSlotSuite, setHarnessHost } from "@entire-vc/mesh-theme-sdk/testing";
import { Draggable, TaskLink } from "@/core/primitives";
import { SlotBadges } from "@/components/extensions/slots/slot-host";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { meshHost } from "@/test-utils/slot-host";
import { resolveSlotView } from "../registry";
import type { BoardCardView, BoardLayoutView } from "../types";
import type { Task } from "@/types";

registerMeshDefaultTheme();
setHarnessHost(meshHost);

const asView = (v: unknown) => v as ComponentType<never>;
const show = (title: string, r: Awaited<ReturnType<typeof runSlotSuite>>) => {
  const text = `${title}\n` + r.tests.map((t) => `  ${t.status === "failed" ? "FAIL" : "ok  "} ${t.id} ${t.error?.split("\n")[0] ?? ""}`).join("\n") + "\n";
  // MESH_RED_LOG=<file> keeps the red run's output for the card comment.
  if (process.env.MESH_RED_LOG) appendFileSync(process.env.MESH_RED_LOG, text);
};
const failedIds = (r: Awaited<ReturnType<typeof runSlotSuite>>) => r.tests.filter((t) => t.status === "failed").map((t) => t.id);

/** A small but contract-complete card; `draggable: false` is the classic mistake (a card that cannot be moved). */
const makeCard = (draggable: boolean): BoardCardView =>
  function Card({ data, actions }) {
    const { task, isOverlay, fields } = data;
    const keys = fields.map((f) => f.key);
    const body = (
      <>
        <TaskLink taskId={task.id}>{task.title}</TaskLink>
        {data.gateStatusName ? <span>{data.gateStatusName}</span> : null}
        {keys.map((k) =>
          k === "labels" ? <span key={k}>{task.labels.join(", ")}</span>
          : k === "cf:sprint" ? <span key={k}>{task.customFields.sprint == null ? null : String(task.customFields.sprint)}</span>
          : k === "priority" ? <span key={k}>{task.priority}</span>
          : k === "due_date" ? <span key={k}>{task.dueDate ?? ""}</span>
          : null,
        )}
        {task.overdue ? <span role="img" aria-label="overdue">!</span> : null}
        <SlotBadges task={{ id: task.id, project_id: task.projectId, title: task.title, vcs_link_count: task.counts.vcsLinks, vcs_providers: task.vcsProviders } as unknown as Task} />
      </>
    );
    void actions;
    if (isOverlay) return <div>{task.title}</div>; // the preview under the pointer: no handlers
    if (!draggable) return <div>{body}</div>;
    return <Draggable item={{ kind: "task", id: task.id }} label={task.title}>{body}</Draggable>;
  };

describe("contract suites go red on broken views and green on the fixed one", () => {
  it("board.card without Draggable fails CT-board-* (not CT-common-*); with Draggable it is green", async () => {
    const red = await runSlotSuite("board.card", asView(makeCard(false)));
    const ids = failedIds(red);
    show("RED board.card (no Draggable):", red);
    expect(red.passed).toBe(false);
    for (const id of ["CT-board-01", "CT-board-02", "CT-board-03", "CT-board-04"]) expect(ids).toContain(id);
    expect(ids.filter((i) => i.startsWith("CT-common"))).toEqual([]);
    const green = await runSlotSuite("board.card", asView(makeCard(true)));
    show("GREEN board.card (fixed):", green);
    expect(failedIds(green)).toEqual([]);
    expect(green.passed).toBe(true);
  });

  it("CT-common-01: a layout that draws nothing in loading/error fails", async () => {
    const Good = resolveSlotView("board.layout") as BoardLayoutView;
    const Empty: BoardLayoutView = (p) => (p.data.columns.state === "ready" ? <Good {...p} /> : null);
    const r = await runSlotSuite("board.layout", asView(Empty));
    expect(failedIds(r)).toContain("CT-common-01");
  });

  it("CT-common-02: a card that throws on a null assignee fails", async () => {
    const Good = resolveSlotView("board.card") as BoardCardView;
    const Throws: BoardCardView = (p) => {
      void (p.data.task.assignee as unknown as { name: string }).name;
      return <Good {...p} />;
    };
    const r = await runSlotSuite("board.card", asView(Throws));
    expect(failedIds(r)).toContain("CT-common-02");
  });

  it("CT-common-03: a card that fetches on its own fails", async () => {
    const Good = resolveSlotView("board.card") as BoardCardView;
    const Leaks: BoardCardView = (p) => {
      useEffect(() => {
        void fetch("/api/v1/leak").catch(() => {});
      }, []);
      return <Good {...p} />;
    };
    const r = await runSlotSuite("board.card", asView(Leaks));
    expect(failedIds(r)).toContain("CT-common-03");
  });

  it("a contract major the suite does not cover is not passed", async () => {
    const r = await runSlotSuite("board.card", asView(resolveSlotView("board.card")), 2);
    expect(r.passed).toBe(false);
  });

  it("overlapping mounts give the original fetch back after cleanup", async () => {
    const original = globalThis.fetch;
    const view = asView(resolveSlotView("board.card"));
    mountSlot(view, { slot: "board.card" });
    mountSlot(view, { slot: "board.card" });
    expect(globalThis.fetch).not.toBe(original);
    cleanupMounts();
    expect(globalThis.fetch).toBe(original);
  });

  it("a view that throws while rendering gives the original fetch back", async () => {
    const original = globalThis.fetch;
    const Boom = (() => {
      throw new Error("boom");
    }) as unknown as ComponentType;
    expect(() => mountSlot(asView(Boom), { slot: "board.card" })).toThrow("boom");
    expect(globalThis.fetch).toBe(original);
  });
});
