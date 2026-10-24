import type { ComponentType } from "react";
import { afterAll, afterEach, describe, it } from "vitest";
import { CONTRACT_VERSION } from "../src/index";
import { cleanupMounts, mountSlot } from "./harness";
import { suiteFor, type ContractTest, type TestCtx } from "./suite";
import type { SlotName } from "./types";

export interface ThemeSlot {
  view: ComponentType<never>;
  /** Major version of the slot contract the theme declares. Only 1 has a suite. */
  major?: number;
}

/** `theme.slots` as the test sees it. */
export type ThemeSlots = Partial<Record<SlotName, ThemeSlot | ComponentType<never>>>;

export interface TestResult {
  id: string;
  title: string;
  status: "passed" | "failed";
  error?: string;
}

export interface SlotResult {
  slot: string;
  tests: TestResult[];
  /** All tests of the slot's suite green for the declared major. */
  passed: boolean;
}

export interface ContractReport {
  contract: string;
  /** `contract_report.passed` of the build report (ADR D9): slot -> contract version it passed. */
  passed: Record<string, string>;
  failed: Record<string, string[]>;
}

const asSlot = (s: ThemeSlot | ComponentType<never>): ThemeSlot =>
  typeof s === "function" || (s as { $$typeof?: symbol }).$$typeof ? { view: s as ComponentType<never> } : (s as ThemeSlot);

const ctxFor = (slot: string, view: ComponentType<never>): TestCtx => ({
  slot: slot as SlotName,
  view,
  mount: (scenario) => mountSlot(view, { ...(scenario ?? {}), slot: slot as SlotName } as never) as never,
});

async function runOne(t: ContractTest, slot: string, view: ComponentType<never>): Promise<TestResult> {
  try {
    await t.run(ctxFor(slot, view));
    return { id: t.id, title: t.title, status: "passed" };
  } catch (e) {
    return { id: t.id, title: t.title, status: "failed", error: e instanceof Error ? e.message : String(e) };
  } finally {
    cleanupMounts();
  }
}

/**
 * Runs the whole suite of one slot against `view` and returns the outcome per test, without registering
 * anything in vitest: the red controls (a view that breaks the contract must fail it) and the build report use it.
 * Call it from inside a test.
 */
export async function runSlotSuite(slot: string, view: ComponentType<never>, major = 1): Promise<SlotResult> {
  if (major !== 1) {
    return { slot, passed: false, tests: [{ id: "CT-common-00", title: `no suite for contract major ${major}`, status: "failed", error: `contract major ${major} is not covered (only 1)` }] };
  }
  const tests: TestResult[] = [];
  for (const t of suiteFor(slot)) tests.push(await runOne(t, slot, view));
  return { slot, tests, passed: tests.length > 0 && tests.every((t) => t.status === "passed") };
}

export function toReport(results: SlotResult[]): ContractReport {
  const report: ContractReport = { contract: CONTRACT_VERSION, passed: {}, failed: {} };
  for (const r of results) {
    if (r.passed) report.passed[r.slot] = CONTRACT_VERSION;
    else report.failed[r.slot] = r.tests.filter((t) => t.status === "failed").map((t) => t.id);
  }
  return report;
}

/**
 * Registers one vitest `describe` per declared slot with the slot's tests; after the last test builds the
 * report (`passed{slot -> x.y.z}`) and hands it to `onReport`. The suite is built from `theme.slots`:
 * a slot the theme does not declare has no tests.
 */
export function registerContractSuites(slots: ThemeSlots, onReport?: (report: ContractReport) => void): void {
  const outcome = new Map<string, Map<string, boolean>>();
  afterEach(() => cleanupMounts());
  for (const [slot, decl] of Object.entries(slots)) {
    const { view, major = 1 } = asSlot(decl as ThemeSlot);
    const seen = new Map<string, boolean>();
    outcome.set(slot, seen);
    describe(`${slot} contract v${CONTRACT_VERSION}`, () => {
      if (major !== 1) {
        it(`CT-common-00 contract major ${major} has no suite`, () => {
          seen.set("CT-common-00", false);
          throw new Error(`contract major ${major} is not covered (only 1)`);
        });
        return;
      }
      for (const t of suiteFor(slot)) {
        it(`${t.id}: ${t.title}`, async () => {
          seen.set(t.id, false);
          await t.run(ctxFor(slot, view));
          seen.set(t.id, true);
        });
      }
    });
  }
  afterAll(() => {
    const results: SlotResult[] = [...outcome].map(([slot, seen]) => {
      const planned = suiteFor(slot);
      const tests = planned.map<TestResult>((t) => ({ id: t.id, title: t.title, status: seen.get(t.id) ? "passed" : "failed" }));
      return { slot, tests, passed: tests.length > 0 && tests.every((t) => t.status === "passed") };
    });
    const report = toReport(results);
    onReport?.(report);
  });
}
