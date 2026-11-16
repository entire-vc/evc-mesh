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
  /** Contract majors the test is written for; absent = every major the slot has a suite for. */
  majors?: readonly number[];
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

/** Contract majors that have a suite for the slot: 1 everywhere; dashboard.layout 2 too (ADR D6: the previous major stays for one release). */
export const suiteMajors = (slot: string): readonly number[] => (slot === "dashboard.layout" ? [1, 2] : [1]);

/** The suite of one slot at one contract major: common tests plus the group's, in id order. */
export function suiteFor(slot: string, major = 1): ContractTest[] {
  return all.filter((t) => appliesTo(t, slot) && (t.majors ?? suiteMajors(slot)).includes(major)).sort((a, b) => a.id.localeCompare(b.id, "en", { numeric: true }));
}
