// Client of the Dashboards extension API (PRD «Дашборды», §6.2 plus shared dashboards).
// Until the backend (card B) is live, the contract is exercised against mocks of these
// exact paths and shapes. Everything that names a path or a field of the wire lives here.
import { api } from "@/lib/api";

export type DashboardPlacement = "top" | "bottom";

export interface Dashboard {
  id: string;
  name: string;
  /** One emoji grapheme or "" (default icon). */
  icon: string;
  placement: DashboardPlacement;
  position: number;
  is_default: boolean;
  /** Not in the database yet (§5.3): `id` is "default". */
  virtual: boolean;
  /** Shared with the workspace: every member sees it (until they hide it). */
  shared: boolean;
  /** Hidden from MY menu (only shared ones can be hidden); still listed on «All dashboards». */
  hidden: boolean;
  can_edit: boolean;
  can_manage: boolean;
  members_can_edit?: boolean;
  author_user_id?: string | null;
  author_name?: string | null;
  version?: number;
  /** Widget types placed on it (the list endpoint carries them so the menu can find the `mentions` one). */
  widget_types?: string[];
}

/** The server sends the id and the widgets only; the labels are the client's (`dashboards.templates.<id>`). */
export interface DashboardTemplate {
  id: string;
  widgets?: unknown[];
}

export interface DashboardOrder {
  top: string[];
  bottom: string[];
}

export interface CreateDashboardInput {
  name: string;
  icon: string;
  placement: DashboardPlacement;
  shared: boolean;
  template_id?: string;
  members_can_edit?: boolean;
}

export const MAX_DASHBOARD_NAME = 60;

const base = (wsId: string) => `/api/v1/workspaces/${wsId}`;

function asList<T>(r: T[] | Record<string, T[]> , key: string): T[] {
  return Array.isArray(r) ? r : (r?.[key] ?? []);
}

/** `includeHidden`: the menu list omits the shared dashboards I hid; «All dashboards» needs them too. */
export async function listDashboards(wsId: string, includeHidden = false): Promise<Dashboard[]> {
  const q = includeHidden ? "?include_hidden=true" : "";
  return asList(await api<Dashboard[] | { dashboards: Dashboard[] }>(`${base(wsId)}/dashboards${q}`), "dashboards");
}

export async function listDashboardTemplates(wsId: string): Promise<DashboardTemplate[]> {
  return asList(
    await api<DashboardTemplate[] | { templates: DashboardTemplate[] }>(`${base(wsId)}/dashboard-templates`),
    "templates",
  );
}

export function createDashboard(wsId: string, input: CreateDashboardInput): Promise<Dashboard> {
  return api<Dashboard>(`${base(wsId)}/dashboards`, { method: "POST", body: input });
}

const one = (wsId: string, id: string) => `${base(wsId)}/dashboards/${id}`;

export function renameDashboard(wsId: string, id: string, name: string): Promise<Dashboard> {
  return api<Dashboard>(one(wsId, id), { method: "PATCH", body: { name } });
}

export function setDashboardIcon(wsId: string, id: string, icon: string): Promise<Dashboard> {
  return api<Dashboard>(one(wsId, id), { method: "PATCH", body: { icon } });
}

/** Sharing lives on the same PATCH as the name and the icon. */
export function setDashboardSharing(
  wsId: string,
  id: string,
  sharing: { shared: boolean; members_can_edit?: boolean },
): Promise<Dashboard> {
  return api<Dashboard>(one(wsId, id), { method: "PATCH", body: sharing });
}

/** Hide a shared dashboard from my menu, or bring it back («All dashboards»). Personal/virtual: 422. */
export function setDashboardHidden(wsId: string, id: string, hidden: boolean): Promise<Dashboard> {
  return api<Dashboard>(`${one(wsId, id)}/hidden`, { method: "PUT", body: { hidden } });
}

export function duplicateDashboard(wsId: string, id: string): Promise<Dashboard> {
  return api<Dashboard>(`${one(wsId, id)}/duplicate`, { method: "POST" });
}

export function resetDashboard(wsId: string, id: string): Promise<Dashboard> {
  return api<Dashboard>(`${one(wsId, id)}/reset`, { method: "POST" });
}

export async function deleteDashboard(wsId: string, id: string): Promise<void> {
  await api<unknown>(one(wsId, id), { method: "DELETE" });
}

/**
 * The whole menu order in one call; the body must be exactly the menu (hidden ones excluded, else 422
 * `dashboard_order_mismatch`). The answer is the new list: a virtual "default" comes back with a real id.
 */
export async function putDashboardOrder(wsId: string, order: DashboardOrder): Promise<Dashboard[]> {
  return asList(
    await api<Dashboard[] | { dashboards: Dashboard[] }>(`${base(wsId)}/dashboards/order`, { method: "PUT", body: order }),
    "dashboards",
  );
}

// -- pure helpers over the list ----------------------------------------------------------------

/** Dashboards of the menu group in order; hidden ones are not in the menu. */
export function menuGroup(list: readonly Dashboard[], group: DashboardPlacement): Dashboard[] {
  return list
    .filter((d) => d.placement === group && !d.hidden)
    .sort((a, b) => a.position - b.position);
}

export function orderOf(list: readonly Dashboard[]): DashboardOrder {
  return {
    top: menuGroup(list, "top").map((d) => d.id),
    bottom: menuGroup(list, "bottom").map((d) => d.id),
  };
}

/** `id` moved to `group` at `index` (index among that group without `id`); the order of the other group is kept. */
export function moveInOrder(order: DashboardOrder, id: string, group: DashboardPlacement, index: number): DashboardOrder {
  const top = order.top.filter((x) => x !== id);
  const bottom = order.bottom.filter((x) => x !== id);
  const target = group === "top" ? top : bottom;
  target.splice(Math.max(0, Math.min(index, target.length)), 0, id);
  return { top, bottom };
}

/** One step up/down inside the group; null when already at the edge. */
export function stepInOrder(order: DashboardOrder, id: string, delta: -1 | 1): DashboardOrder | null {
  const group: DashboardPlacement = order.top.includes(id) ? "top" : "bottom";
  const at = order[group].indexOf(id);
  const to = at + delta;
  if (at === -1 || to < 0 || to >= order[group].length) return null;
  return moveInOrder(order, id, group, to);
}

/** Applies an order to the list: placement and position follow it; ids not in it (hidden) are untouched. */
export function applyOrder(list: readonly Dashboard[], order: DashboardOrder): Dashboard[] {
  const pos = new Map<string, { placement: DashboardPlacement; position: number }>();
  order.top.forEach((id, i) => pos.set(id, { placement: "top", position: i }));
  order.bottom.forEach((id, i) => pos.set(id, { placement: "bottom", position: i }));
  return list.map((d) => (pos.has(d.id) ? { ...d, ...pos.get(d.id)! } : d));
}

/** A virtual default dashboard carries the standard four widgets, `mentions` among them (§5.3). */
export function hasMentionsWidget(d: Dashboard): boolean {
  return d.widget_types ? d.widget_types.includes("mentions") : d.virtual;
}

/** The dashboard the unread-mentions counter lives on: first in the menu (top, then bottom) with a `mentions` widget. */
export function firstMentionsDashboardId(list: readonly Dashboard[]): string | null {
  const ordered = [...menuGroup(list, "top"), ...menuGroup(list, "bottom")];
  return ordered.find(hasMentionsWidget)?.id ?? null;
}
