import { createContext, useContext, type FC, type ReactNode } from "react";
import type { TaskViewBlockId, TaskViewData } from "@/core/slots/types";

/**
 * What TaskPanel hands to a `task.view` view: ready elements bound to the open task. The view places
 * them; it does not build them. The block components below have one identity for the life of the app and
 * read the latest element from context, so a layout switch or a re-render never remounts a block (the
 * comment being typed lives inside the element's own component and survives).
 */
export interface PanelBlocks {
  elements: Partial<Record<TaskViewBlockId, ReactNode>>;
  topBar: (openPanel: (extensionId: string) => void) => ReactNode;
  dependencyBadge: ReactNode;
  extensionPanel: (props: { extensionId: string; name: string; external?: boolean }) => ReactNode;
}

export const PanelBlocksContext = createContext<PanelBlocks | null>(null);

function makeBlock(id: TaskViewBlockId): FC {
  const Block: FC = () => <>{useContext(PanelBlocksContext)?.elements[id] ?? null}</>;
  Block.displayName = `TaskPanelBlock(${id})`;
  return Block;
}

const TopBar: FC<{ openPanel(extensionId: string): void }> = ({ openPanel }) => <>{useContext(PanelBlocksContext)?.topBar(openPanel) ?? null}</>;
const ExtensionPanel: FC<{ extensionId: string; name: string; external?: boolean }> = (props) => <>{useContext(PanelBlocksContext)?.extensionPanel(props) ?? null}</>;
const DependencyBadge: FC = () => <>{useContext(PanelBlocksContext)?.dependencyBadge ?? null}</>;

export const TASK_VIEW_COMPONENTS: TaskViewData["components"] = {
  header: makeBlock("header"),
  description: makeBlock("description"),
  properties: makeBlock("properties"),
  subtasks: makeBlock("subtasks"),
  dependencies: makeBlock("dependencies"),
  comments: makeBlock("comments"),
  artifacts: makeBlock("artifacts"),
  vcs: makeBlock("vcs"),
  activity: makeBlock("activity"),
  TopBar,
  DependencyBadge,
  ExtensionPanel,
};
