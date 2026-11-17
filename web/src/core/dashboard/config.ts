// Per-instance widget settings (limit, onlyNew, projectId, ...). <Widget> passes `instance.settings` here for the v1
// widgets (data hook + view); hooks read it from here, views never fetch.
import { createContext, useContext } from "react";

export type WidgetConfig = Record<string, unknown>;
const EMPTY: WidgetConfig = {};

export const WidgetConfigContext = createContext<WidgetConfig>(EMPTY);
export const useWidgetConfig = (): WidgetConfig => useContext(WidgetConfigContext);

export function numberSetting(c: WidgetConfig, key: string, def: number, min: number, max: number): number {
  const v = c[key];
  return typeof v === "number" && Number.isFinite(v) ? Math.min(max, Math.max(min, Math.round(v))) : def;
}
export const boolSetting = (c: WidgetConfig, key: string, def = false): boolean => (typeof c[key] === "boolean" ? (c[key] as boolean) : def);
export const stringSetting = (c: WidgetConfig, key: string): string | undefined => (typeof c[key] === "string" && c[key] ? (c[key] as string) : undefined);

/** The instance title from its settings (§5.6: the title replaces the type name), else the widget's own name. */
export function widgetTitle(c: WidgetConfig, fallback: string): string {
  const v = c.title;
  return typeof v === "string" && v.trim() ? v.trim() : fallback;
}
export const useWidgetTitle = (fallback: string): string => widgetTitle(useWidgetConfig(), fallback);
