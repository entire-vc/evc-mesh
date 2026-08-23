import { afterEach, describe, expect, it, vi } from "vitest";
import { makeDocEditor } from "@/lib/milkdown/editor";

/**
 * REGRESSION (evc-mesh #c4112e5e): `@milkdown/ctx`'s internal readiness
 * `Timer` (used by every `ctx.wait(...)`, e.g. InitReady/SchemaReady/
 * EditorViewReady) races an event listener against a `setTimeout` fallback,
 * but only ever cancels the LISTENER on early success — the `setTimeout` was
 * left scheduled regardless, and fired ~3s later calling a bare (global-
 * scoped) `removeEventListener` a second time. In production that's a
 * harmless no-op; in a test runner that tears the DOM/global environment
 * down between files, a stray call landing after teardown throws
 * `ReferenceError: removeEventListener is not defined` — which is exactly
 * what made `open-does-not-rewrite.test.ts` fail intermittently on green
 * runs (vitest attributes the error to whichever file's environment was
 * torn down when the ~3s-old timer finally fired, not to a real assertion).
 *
 * Patched via `patches/@milkdown__ctx@7.22.1.patch` (pnpm patch) to cancel
 * the pending timeout the moment the listener resolves — symmetric cleanup,
 * same principle as always removing a listener you added.
 *
 * This test proves the fix at its source (no leftover timer) rather than
 * racing the ~3s window in real time or depending on jsdom teardown order,
 * which is what made the original failure a flake in the first place.
 */
describe("milkdown editor destroy does not leak @milkdown/ctx readiness timers", () => {
  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  it("cancels every pending readiness timeout on destroy", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    document.body.appendChild(root);
    const editor = makeDocEditor({ root, defaultValue: "hello\n", editable: true });

    await editor.create();
    await editor.destroy();

    expect(
      vi.getTimerCount(),
      "editor.destroy() must cancel every readiness timer it started, not just the listener",
    ).toBe(0);
  });
});
