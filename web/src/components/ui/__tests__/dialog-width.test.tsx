import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { Dialog, DialogContent } from "@/components/ui/dialog";

/**
 * The centring wrapper caps a dialog's width, and a `max-w-*` on DialogContent
 * is silently clamped by it — which is how the artifact viewer ended up 480px
 * wide while asking for `max-w-4xl`. The override added for that must not have
 * moved any other dialog: every existing caller passes no className and has to
 * stay at `max-w-lg`.
 */
describe("Dialog wrapper width", () => {
  function wrapperOf(className?: string) {
    render(
      <Dialog open onOpenChange={vi.fn()} className={className}>
        <DialogContent data-testid="c">body</DialogContent>
      </Dialog>,
    );
    return document.querySelector('[data-testid="c"]')?.parentElement;
  }

  it("keeps max-w-lg when no override is given", () => {
    expect(wrapperOf()?.className).toContain("max-w-lg");
  });

  it("takes the override when one is given, and drops the default", () => {
    const cls = wrapperOf("max-w-4xl")?.className ?? "";
    expect(cls).toContain("max-w-4xl");
    // tailwind-merge must have removed the old value, not stacked both — two
    // competing max-widths would resolve by stylesheet order, not by intent.
    expect(cls).not.toContain("max-w-lg");
  });
});
