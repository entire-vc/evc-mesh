import { createContext, useContext, type ReactNode } from "react";
import type { SlotContext } from "./types";

const Ctx = createContext<SlotContext | null>(null);

/** Puts the slot's `ctx` where nested components of a theme's view can read it (`useSlotContext`). */
export function SlotContextProvider({ value, children }: { value: SlotContext; children: ReactNode }) {
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

/** SDK hook: the context of the slot being rendered, the same object the view got as `props.ctx`. */
export function useSlotContext(): SlotContext {
  const v = useContext(Ctx);
  if (!v) throw new Error("useSlotContext() outside a slot view");
  return v;
}
