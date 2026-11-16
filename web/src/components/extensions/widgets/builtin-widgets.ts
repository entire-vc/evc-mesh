import { lazy, type ComponentType, type LazyExoticComponent } from "react";

/**
 * Dashboard widgets of built-in extensions, by the name their manifest gives in
 * `widgets[].render.builtin`. Each is a lazy chunk: a dashboard without the
 * widget never loads its code. The registry of the extension (enabled, slot,
 * consent) decides *whether* it draws; this file decides *what* draws.
 */
export interface ExtensionWidgetProps {
  /** Full widget type, `<ext_id>.<widget_id>`. */
  type: string;
  /** The instance's settings (the manifest's `widgets[].settings` schema). */
  settings: Record<string, unknown>;
  /** The instance's size in grid cells. */
  size?: { w: number; h: number };
}

export const BUILTIN_WIDGETS: Record<string, LazyExoticComponent<ComponentType<ExtensionWidgetProps>>> = {
  GitlabFailedPipelinesWidget: lazy(() => import("./gitlab-failed-pipelines")),
};
