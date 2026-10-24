import type { ComponentType } from "react";
import type { Mounted, Scenario, SlotName } from "./types";

/** What a contract test gets: the view under test and a mount bound to its slot. */
export interface TestCtx {
  slot: SlotName;
  view: ComponentType<never>;
  /** `mountSlot(view, { slot, ...scenario })` */
  mount<D = unknown>(scenario?: Omit<Scenario<D>, "slot">): Mounted<D>;
}

export interface ContractTest {
  /** `CT-<group>-NN` as in the catalog (Mesh Doc theme-slot-catalog-v1 §6 and the slots-* docs). */
  id: string;
  title: string;
  /** Slots whose suite contains the test. A test that does not apply to a slot is not in its suite. */
  slots: readonly string[];
  run(t: TestCtx): void | Promise<void>;
}

/** Suite files push their tests here; `suitesFor(slot)` reads them back. */
const all: ContractTest[] = [];

export function defineTests(tests: ContractTest[]): void {
  all.push(...tests);
}

export function allTests(): readonly ContractTest[] {
  return all;
}

const appliesTo = (t: ContractTest, slot: string) =>
  t.slots.includes(slot) || (slot.startsWith("dashboard.widget.") && t.slots.includes("dashboard.widget.*"));

/** The suite of one slot: common tests plus the group's, in id order. */
export function suiteFor(slot: string): ContractTest[] {
  return all.filter((t) => appliesTo(t, slot)).sort((a, b) => a.id.localeCompare(b.id, "en", { numeric: true }));
}
