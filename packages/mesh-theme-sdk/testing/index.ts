// @entire-vc/mesh-theme-sdk/testing: contract tests for theme slot views.
export { setHarnessHost } from "./host";
export { mountSlot, cleanupMounts } from "./harness";
export { registerContractSuites, runSlotSuite, toReport } from "./runner";
export type { ContractReport, SlotResult, TestResult, ThemeSlot, ThemeSlots } from "./runner";
export { fixtures } from "./fixtures";
export { defineTests, suiteFor } from "./suite";
export type { ContractTest, TestCtx } from "./suite";
export type * from "./types";
import "./suites";
import "./mounters";
