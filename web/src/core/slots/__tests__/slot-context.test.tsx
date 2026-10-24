import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { Slot, registerSlotView } from "../registry";
import { useSlotContext } from "../slot-context";
import type { SlotContext, SlotView } from "../types";

const ctx: SlotContext = {
  slot: "ctx.slot",
  contractVersion: "1.0.0",
  locale: "en",
  mode: "light",
  isMobile: false,
  workspace: { id: "w1", slug: "acme", name: "Acme" },
};

afterEach(cleanup);

describe("useSlotContext (SDK hook)", () => {
  it("gives a component nested in a view the same ctx the view got as props.ctx", () => {
    let seen: SlotContext | null = null;
    let own: SlotContext | null = null;
    const Inner = () => {
      seen = useSlotContext();
      return <p>nested {seen.slot}</p>;
    };
    const View: SlotView<null, null> = (p) => {
      own = p.ctx;
      return <Inner />;
    };
    registerSlotView("ctx.slot", "mesh-default", View as never);
    render(<Slot name="ctx.slot" data={null} actions={null} themeSettings={{}} ctx={ctx} />);
    expect(screen.getByText("nested ctx.slot")).toBeTruthy();
    expect(seen).toBe(own);
    expect(seen).toBe(ctx);
  });

  it("throws outside a slot view", () => {
    const Bare = () => {
      useSlotContext();
      return null;
    };
    const quiet = console.error;
    console.error = () => {};
    try {
      expect(() => render(<Bare />)).toThrow(/outside a slot view/);
    } finally {
      console.error = quiet;
    }
  });
});
