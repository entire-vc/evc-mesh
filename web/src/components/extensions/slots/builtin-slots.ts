import { lazy, type ComponentType, type LazyExoticComponent } from "react";
import type { Task, VCSProvider } from "@/types";
import type { SlotId } from "@/lib/extension-slots";
import { GitLabBadge } from "./gitlab-badge";

/**
 * What a built-in extension puts into each slot. A contribution is named by the
 * extension id the registry reports; the registry decides *whether* it shows
 * (connected, enabled, slot declared in the manifest), this file decides *what*.
 */
export interface PanelProps {
  task: Task;
}
export interface BadgeProps {
  task: Task;
}
export interface ActionContext {
  task: Task;
  /** Switch the task view to this extension's `task.panel` tab. */
  openPanel: (extensionId: string) => void;
}
export interface SlotAction {
  id: string;
  /** i18n key in the `task` namespace. */
  labelKey: string;
  run: (ctx: ActionContext) => void;
}

export interface BuiltinContribution {
  /** VCS provider whose links this extension's panel owns (hidden from the generic list). */
  ownsVcsProvider?: VCSProvider;
  "task.panel"?: LazyExoticComponent<ComponentType<PanelProps>>;
  /** Tiny and synchronous on purpose: it is rendered once per board card. */
  "task.badge"?: ComponentType<BadgeProps>;
  "task.actions"?: SlotAction[];
}

const GitLabPanel = lazy(() => import("./gitlab-panel"));

export const BUILTIN_SLOTS: Record<string, BuiltinContribution> = {
  gitlab: {
    ownsVcsProvider: "gitlab",
    "task.panel": GitLabPanel,
    "task.badge": GitLabBadge,
    "task.actions": [
      {
        id: "gitlab.open-panel",
        labelKey: "slots.gitlab.showMergeRequests",
        run: ({ openPanel }) => openPanel("gitlab"),
      },
    ],
  },
};

/** Providers whose links belong to an extension panel, not the generic VCS list. */
export const SLOT_OWNED_VCS_PROVIDERS: VCSProvider[] = Object.values(BUILTIN_SLOTS)
  .map((c) => c.ownsVcsProvider)
  .filter((p): p is VCSProvider => Boolean(p));

export type { SlotId };
