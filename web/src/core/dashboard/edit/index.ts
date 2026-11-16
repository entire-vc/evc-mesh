// Single entry point of the edit mode. P mounts <LazyDashboardEditor/> and <EditGear/>; the heavy parts
// (react-grid-layout, catalog, SchemaForm dialog) sit behind a dynamic import in edit-gear.tsx.
export { EditGear, LazyDashboardEditor as DashboardEditor } from "./edit-gear";
export type { DashboardEditorProps, DashboardLayout, LayoutItem, CatalogType, LayoutApi, ThemeLayoutContract } from "./types";
export { StaleLayoutError } from "./types";
export { layoutFromDoc, workspaceApi } from "./api";
