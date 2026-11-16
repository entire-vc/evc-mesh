// The old group shape: views exported next to the descriptors, i.e. eagerly. registry.test.ts must catch it.
import type { WidgetDescriptor } from "../../../../types";

export const descriptors: WidgetDescriptor[] = [
  {
    id: "fixture.eager", title: "Eager", description: "", category: "test", origin: "core", scope: "workspace", actions: [],
    size: { default: { w: 4, h: 2 }, min: { w: 2, h: 1 }, max: { w: 12, h: 4 } },
  },
];
export const views = { "fixture.eager": () => null };
