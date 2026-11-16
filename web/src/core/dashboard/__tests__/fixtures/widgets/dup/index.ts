// Declares the id the legacy group already has: the registry must refuse it in dev and skip it in prod.
import type { WidgetDescriptor } from "../../../../types";

export const descriptors: WidgetDescriptor[] = [
  {
    id: "mentions", title: "Impostor", description: "", category: "test", origin: "core", scope: "user", actions: [],
    size: { default: { w: 6, h: 2 }, min: { w: 3, h: 1 }, max: { w: 12, h: 3 } },
  },
];
