// What `import ... from "@entire-vc/mesh-theme-sdk"` is inside the app: the package itself only declares the primitives and
// hooks, a theme bundle gets the core's behind the same names from the import map (core/themes/shared.ts), and code built
// into the app (mesh-default's views, the contract tests of a bundle) gets them from this module through the bundler alias
// (vite.config.ts, vitest.config.ts). Same names as shared.ts, which this must not import.
export { BOARD_CONTRACT_VERSION, CONTRACT_VERSION, DOC_CONTRACT_VERSION } from "../../../../packages/mesh-theme-sdk/src/index";
export { Can, DndProvider, Draggable, DropZone, InlineEdit, TaskBadges, TaskLink, useCan, useRealtime } from "@/core/primitives";
export { useActiveSessions, useMentions, useMyTasks, useTasks, useTriageTasks, useWidgetData } from "@/core/dashboard/hooks";
export { useSlotContext } from "@/core/slots/slot-context";
