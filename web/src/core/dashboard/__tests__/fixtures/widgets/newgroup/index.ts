// A widget group as W1..W6 add one: a folder with index.ts (`descriptors`) and views.tsx (`views`), nothing else touched.
import type { WidgetDescriptor } from "../../../../types";

export const descriptors: WidgetDescriptor[] = [
  {
    id: "fixture.counter", title: "Counter", description: "A fixture widget", category: "test", origin: "core", scope: "workspace", actions: [],
    size: { default: { w: 4, h: 2 }, min: { w: 2, h: 1 }, max: { w: 12, h: 4 } },
  },
];
