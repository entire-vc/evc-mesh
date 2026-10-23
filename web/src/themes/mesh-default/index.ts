import { registerDefaultSlotView } from "@/core/slots/registry";
import { BoardCard } from "./board/card";
import { BoardColumn } from "./board/column";
import { BoardLayout } from "./board/layout";
import { DocTreeMeshDefault } from "./doc/tree";
import { DocViewMeshDefault } from "./doc/view";
import { TaskRow } from "./list/task-row";
import { TaskView } from "./task/view";

/** The built-in theme: every slot's default view. */
export function registerMeshDefaultTheme(): void {
  registerDefaultSlotView("board.layout", BoardLayout);
  registerDefaultSlotView("board.column", BoardColumn);
  registerDefaultSlotView("board.card", BoardCard);
  registerDefaultSlotView("task.row", TaskRow);
  registerDefaultSlotView("task.view", TaskView);
  registerDefaultSlotView("doc.tree", DocTreeMeshDefault);
  registerDefaultSlotView("doc.view", DocViewMeshDefault);
}
