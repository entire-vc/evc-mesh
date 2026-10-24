import type { SlotMounter } from "../types";

const mounters = new Map<string, SlotMounter<never>>();

export function registerMounter<D>(m: SlotMounter<D>): void {
  mounters.set(m.slot, m as unknown as SlotMounter<never>);
}

export function mounterFor(slot: string): SlotMounter<never> {
  // `dashboard.widget.<id>` shares one mounter.
  const m = mounters.get(slot) ?? (slot.startsWith("dashboard.widget.") ? mounters.get("dashboard.widget") : undefined);
  if (!m) throw new Error(`mesh-theme-sdk/testing: no mounter for slot "${slot}"`);
  return m;
}
