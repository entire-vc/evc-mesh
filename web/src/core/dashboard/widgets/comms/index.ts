// «Коммуникации»: my-comments and recent-comments. `mentions` is drawn by MentionsWidgetView too, but stays in the
// legacy group so the four default widgets keep their place. All three use the v1 widget contract (data hook + view);
// the views are in views.tsx, shipped with the host (see registry.ts).
import type { WidgetDescriptor } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";

const ACTIONS = ["openComment", "loadMore", "retry"];

export const descriptors: WidgetDescriptor[] = [
  { id: "my-comments", title: "My comments", description: "Comments you wrote", category: coreCategory("my-comments"), origin: "core", scope: "user", dataHook: "useMyComments", actions: ACTIONS, size: coreSize("my-comments") },
  { id: "recent-comments", title: "Recent comments", description: "Latest comments in the workspace", category: coreCategory("recent-comments"), origin: "core", scope: "workspace", dataHook: "useRecentComments", actions: ACTIONS, size: coreSize("recent-comments") },
];
