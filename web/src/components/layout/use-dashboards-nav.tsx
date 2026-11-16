import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useSearchParams } from "react-router";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import {
  duplicateDashboard,
  firstMentionsDashboardId,
  menuGroup,
  setDashboardHidden,
  type Dashboard,
  type DashboardPlacement,
} from "@/lib/dashboards";
import { useDashboardsStore } from "@/stores/dashboards";
import { useWebSocketStore } from "@/stores/websocket";
import type { DashboardDialogState } from "./dashboard-dialogs";
import { NEW_DASHBOARD_PARAM } from "./legacy-page-redirect";
import type { NavItem, NavSidebarActions, NavSidebarData } from "@/core/slots/nav-sidebar/types";

// The dialogs (form, icon picker) load on the first open: the menu itself ships with the shell.
const DashboardDialogs = lazy(() => import("./dashboard-dialogs").then((m) => ({ default: m.DashboardDialogs })));

/** Where a dashboard lives: every dashboard, the virtual default included, opens at `/d/:id`
 * (the page route redirects `/dashboard` there too, so the row stays highlighted on arrival). */
export function dashboardHref(wsSlug: string, d: Pick<Dashboard, "id">): string {
  return `/w/${wsSlug}/d/${d.id}`;
}

interface Args {
  workspace: { id: string; slug: string } | null;
  path: string;
  unseenCount: number;
}

type DashboardActions = Pick<
  NavSidebarActions,
  | "reorderDashboards"
  | "createDashboard"
  | "renameDashboard"
  | "changeDashboardIcon"
  | "duplicateDashboard"
  | "shareDashboard"
  | "resetDashboard"
  | "deleteDashboard"
  | "hideDashboard"
>;

export interface DashboardsNav {
  state: NonNullable<NavSidebarData["dashboardsState"]>;
  items: NavItem[];
  allHref: string | undefined;
  /** The mention counter sits on the first dashboard with a `mentions` widget; false = it stays where it was. */
  mentionsMoved: boolean;
  actions: DashboardActions;
  dialogs: React.ReactNode;
}

export function useDashboardsNav({ workspace, path, unseenCount }: Args): DashboardsNav {
  const { t } = useTranslation("shell");
  const navigate = useNavigate();
  const wsId = workspace?.id;
  const wsSlug = workspace?.slug;
  const { items: list, status, load, reorder } = useDashboardsStore();
  const lastEvent = useWebSocketStore((s) => s.lastEvent);
  const connect = useWebSocketStore((s) => s.connect);
  const [dialog, setDialog] = useState<DashboardDialogState>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const newTemplate = searchParams.get(NEW_DASHBOARD_PARAM);

  // `?new-dashboard=<template>` (an old page's address, legacy-page-redirect.tsx): open the create dialog with
  // that template, then drop the parameter so a reload or «Back» does not open it again.
  useEffect(() => {
    if (!newTemplate || !wsId) return;
    setDialog({ kind: "create", group: "bottom", templateId: newTemplate });
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete(NEW_DASHBOARD_PARAM);
        return next;
      },
      { replace: true },
    );
  }, [newTemplate, wsId, setSearchParams]);

  useEffect(() => {
    if (wsId) void load(wsId);
  }, [wsId, load]);
  useEffect(() => {
    if (wsSlug) connect(wsSlug);
  }, [wsSlug, connect]);

  // A shared dashboard renamed, reshared or deleted by someone else: the menu follows without a reload.
  useEffect(() => {
    if (wsId && lastEvent?.type.startsWith("dashboard.")) void load(wsId, { silent: true });
  }, [lastEvent, wsId, load]);

  const reload = useCallback(() => {
    if (wsId) void load(wsId, { silent: true });
  }, [wsId, load]);

  const fail = useCallback((err: unknown) => toast.error(apiErrorMessage(err, t("dashboards.actionFailed"))), [t]);

  const actions = useMemo<DashboardActions>(
    () => ({
      reorderDashboards: async (order) => {
        try {
          await reorder(order);
        } catch (err) {
          fail(err);
        }
      },
      createDashboard: (group: DashboardPlacement) => setDialog({ kind: "create", group }),
      renameDashboard: (id) => setDialog({ kind: "rename", id }),
      changeDashboardIcon: (id) => setDialog({ kind: "icon", id }),
      shareDashboard: (id) => setDialog({ kind: "share", id }),
      resetDashboard: (id) => setDialog({ kind: "reset", id }),
      deleteDashboard: (id) => setDialog({ kind: "delete", id }),
      duplicateDashboard: (id) => {
        duplicateDashboard(wsId!, id)
          .then((d) => {
            reload();
            if (wsSlug) navigate(dashboardHref(wsSlug, d));
          })
          .catch(fail);
      },
      hideDashboard: (id) => {
        setDashboardHidden(wsId!, id, true).then(reload).catch(fail);
      },
    }),
    [reorder, fail, reload, navigate, wsSlug, wsId],
  );

  const mentionsId = status === "ready" ? firstMentionsDashboardId(list) : null;
  const ready = status === "ready" && !!wsSlug;

  const items = useMemo<NavItem[]>(() => {
    if (!ready || !wsSlug) return [];
    const total = list.length;
    const mk = (d: Dashboard, group: DashboardPlacement): NavItem => {
      const href = dashboardHref(wsSlug, d);
      return {
        id: `dash:${d.id}`,
        group: group === "top" ? "dashboards-top" : "dashboards-bottom",
        icon: "LayoutDashboard",
        label: d.name,
        href,
        active: path === href || path.startsWith(`${href}/`) || (d.virtual && path.endsWith("/dashboard")),
        origin: "core",
        inRail: true,
        badge: d.id === mentionsId && unseenCount > 0 ? { count: unseenCount, tone: "attention" } : null,
        dashboard: {
          id: d.id,
          emoji: d.icon,
          shared: d.shared,
          isDefault: d.is_default || d.virtual,
          virtual: d.virtual,
          canEdit: d.can_edit,
          canManage: d.can_manage,
          canDelete: total > 1,
        },
      };
    };
    return [...menuGroup(list, "top").map((d) => mk(d, "top")), ...menuGroup(list, "bottom").map((d) => mk(d, "bottom"))];
  }, [ready, wsSlug, list, path, mentionsId, unseenCount]);

  const dialogs = wsId && dialog ? (
    <Suspense fallback={null}>
      <DashboardDialogs
      state={dialog}
      onClose={() => setDialog(null)}
      workspaceId={wsId}
      dashboards={list}
      onChanged={(openId) => {
        reload();
        const created = openId ? list.find((d) => d.id === openId) : undefined;
        if (openId && wsSlug) navigate(dashboardHref(wsSlug, created ?? { id: openId }));
      }}
      />
    </Suspense>
  ) : null;

  return {
    state: status === "ready" ? "ready" : status === "error" ? "error" : "loading",
    items,
    allHref: wsSlug ? `/w/${wsSlug}/dashboards` : undefined,
    mentionsMoved: mentionsId != null,
    actions,
    dialogs,
  };
}
