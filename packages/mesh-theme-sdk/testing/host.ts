import type { HarnessHost } from "./types";

let current: HarnessHost | null = null;

/** The host the suites run on; set once per test file (see `web/src/test-utils/slot-host.tsx`). */
export function setHarnessHost(host: HarnessHost): void {
  current = host;
}

export function harnessHost(): HarnessHost {
  if (!current) throw new Error("mesh-theme-sdk/testing: call setHarnessHost(host) before mounting a slot");
  return current;
}
