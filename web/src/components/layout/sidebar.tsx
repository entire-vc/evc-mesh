import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useLocation, useNavigate, useParams } from "react-router";
import { fetchUnseenMentionCount } from "@/lib/mentions/inbox";
import { useWorkspaceStore } from "@/stores/workspace";
import { useCapabilitiesStore } from "@/stores/capabilities";
import { useProjectStore } from "@/stores/project";
import { useAuthStore } from "@/stores/auth";
import { useWebSocketStore } from "@/stores/websocket";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { CreateProjectDialog } from "@/components/create-project-dialog";
import { apiErrorMessage } from "@/lib/api-error";
import { useSidebarItems } from "@/lib/extension-slots";
import "@/core/slots/mesh-default";
import { resolveSlotView, useSlotRegistryVersion, useThemeSettings, withSlotFallback } from "@/core/slots/registry";
import { useBuildSlotContext } from "@/core/slots/use-slot-context";
import type { Async } from "@/core/slots/types";
import type {
  NavItem,
  NavProject,
  NavSidebarActions,
  NavSidebarData,
} from "@/core/slots/nav-sidebar/types";
import type { Project } from "@/types";

interface SidebarProps {
  collapsed: boolean;
  onToggle?: () => void;
}

// The container of the `nav.sidebar` slot: the stores, permissions, the mention counter and
// the two dialogs live here; what is drawn lives in the active theme's view
// (`core/slots/nav-sidebar/mesh-default-view.tsx` by default).
export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const { t } = useTranslation("shell");
  const { wsSlug, projectSlug } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const { workspaces, currentWorkspace, createWorkspace, isLoading: workspacesLoading } = useWorkspaceStore();
  const { projects: allProjects, unarchiveProject, fetchProjects, isLoading: projectsLoading, error: projectsError } = useProjectStore();
  // Flat /t/ and /d/ links have no workspace until the resolver redirects.
  // Only actual requests are pending; a dead-end resolver may never select one.
  const navigationPending = workspacesLoading || projectsLoading;
  // The store keeps archived projects so ids still resolve to names elsewhere;
  // the sidebar lists only live ones and tucks the rest under "Archived"
  // (an internal ticket — an archived project used to stay in the list as if nothing
  // had happened).
  const projects = allProjects.filter((p) => !p.is_archived);
  const archivedProjects = allProjects.filter((p) => p.is_archived);
  const [unarchiveError, setUnarchiveError] = useState<string | null>(null);
  const { user } = useAuthStore();
  const lastEvent = useWebSocketStore((s) => s.lastEvent);
  const wsSubscribe = useWebSocketStore((s) => s.subscribe);
  const wsUnsubscribe = useWebSocketStore((s) => s.unsubscribe);
  const [showCreateProject, setShowCreateProject] = useState(false);
  const [createWsOpen, setCreateWsOpen] = useState(false);
  const [wsName, setWsName] = useState("");
  const [wsSlugDraft, setWsSlugDraft] = useState("");
  const [wsCreating, setWsCreating] = useState(false);
  const [wsError, setWsError] = useState<string | null>(null);
  const [unseenCount, setUnseenCount] = useState(0);
  const unseenFetchedRef = useRef<string | null>(null);
  const sparkEnabled = useCapabilitiesStore((s) => s.sparkEnabled);
  const fetchCapabilities = useCapabilitiesStore((s) => s.fetch);
  const extensionItems = useSidebarItems();

  useEffect(() => {
    fetchCapabilities();
  }, [fetchCapabilities]);

  const handleWsNameChange = useCallback((value: string) => {
    setWsName(value);
    setWsSlugDraft(value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""));
  }, []);

  const handleCreateWs = useCallback(async () => {
    if (!wsName.trim() || !wsSlugDraft.trim()) return;
    setWsError(null);
    setWsCreating(true);
    try {
      const ws = await createWorkspace({ name: wsName.trim(), slug: wsSlugDraft.trim() });
      setCreateWsOpen(false);
      setWsName("");
      setWsSlugDraft("");
      navigate(`/w/${ws.slug}`);
    } catch (err) {
      setWsError(apiErrorMessage(err, t("createWorkspace.failed")));
    } finally {
      setWsCreating(false);
    }
  }, [wsName, wsSlugDraft, createWorkspace, navigate, t]);

  // Fetch unseen mention count for the current workspace, then refresh on
  // cache invalidation. Both inboxes: a badge counting only task mentions is
  // the same contradiction the Mentions tab had — the bell says two, the
  // sidebar says none.
  //
  // Re-runs on workspace change (keyed by id, not just "once on mount"): the
  // badge exists on this same sidebar next to a workspace-scoped mention
  // list, and a stale cross-workspace count left over from wherever the user
  // was before is exactly the bug this scoping fixes.
  useEffect(() => {
    const workspaceId = currentWorkspace?.id;
    if (!workspaceId || unseenFetchedRef.current === workspaceId) return;
    unseenFetchedRef.current = workspaceId;
    fetchUnseenMentionCount(workspaceId)
      .then((count) => {
        localStorage.setItem("mesh_unseen_ts", String(Date.now()));
        localStorage.setItem("mesh_unseen_count", String(count));
        setUnseenCount(count);
      })
      .catch(() => {});
  }, [currentWorkspace?.id]);

  // Subscribe to personal WS channel for mention.created events.
  useEffect(() => {
    if (!user?.id) return;
    const channel = `ws:user:${user.id}`;
    wsSubscribe(channel);
    return () => wsUnsubscribe(channel);
  }, [user?.id, wsSubscribe, wsUnsubscribe]);

  // Increment badge on an incoming mention.
  //
  // The server publishes "mention.badge" for both task and document mentions
  // (comment_service.go, document_comment_mentions.go); "mention.created" is
  // what this listener was originally written against and no publisher has ever
  // emitted it. Both names are accepted rather than one renamed, so this stays
  // correct whichever the server sends.
  useEffect(() => {
    if (lastEvent?.type === "mention.badge" || lastEvent?.type === "mention.created") {
      setUnseenCount((n) => n + 1);
    }
  }, [lastEvent]);

  // Clear badge when ActivityPage marks all visible mentions as shown.
  useEffect(() => {
    const handler = (e: Event) => {
      setUnseenCount((e as CustomEvent<{ newCount: number }>).detail.newCount);
    };
    window.addEventListener("mesh:mentions:shown", handler);
    return () => window.removeEventListener("mesh:mentions:shown", handler);
  }, []);

  const path = location.pathname;
  const ws = wsSlug ? `/w/${wsSlug}` : null;
  const wsHref = (suffix: string) => (ws ? `${ws}/${suffix}` : "/");

  const items: NavItem[] = [];
  const core = (
    id: string,
    group: NavItem["group"],
    icon: string,
    label: string,
    href: string,
    active: boolean,
    extra: Partial<NavItem> = {},
  ) => items.push({ id, group, icon, label, href, active, origin: "core", inRail: true, ...extra });
  core("dashboard", "top", "LayoutDashboard", t("nav.dashboard"), wsHref("dashboard"), path.endsWith("/dashboard"));
  core("initiatives", "inbox", "Target", t("nav.initiatives"), wsHref("initiatives"), path.endsWith("/initiatives"));
  core("triage", "inbox", "Inbox", t("nav.triage"), wsHref("triage"), path.endsWith("/triage"));
  core("activity", "inbox", "Bell", t("nav.activity"), wsHref("activity"), path.includes("/activity"), {
    badge: { count: unseenCount, tone: "attention" },
  });
  core("team", "tools", "Bot", t("nav.team"), wsHref("org-chart"), path.endsWith("/org-chart"));
  core("memory", "tools", "Brain", t("nav.memory"), wsHref("memories"), path.endsWith("/memories"));
  core("sessions", "tools", "MonitorDot", t("nav.sessions"), wsHref("sessions"), path.endsWith("/sessions"));
  // Hidden when the server has MESH_SPARK_ENABLED=false.
  if (sparkEnabled) core("spark", "tools", "Sparkles", t("nav.spark"), wsHref("spark"), path.endsWith("/spark"));
  core("analytics", "tools", "BarChart2", t("nav.analytics"), wsHref("analytics"), path.endsWith("/analytics"));
  core("events", "tools", "Activity", t("nav.events"), wsHref("events"), path.endsWith("/events"));
  core("extensions", "tools", "Plug", t("nav.extensions"), wsHref("extensions"), path.endsWith("/extensions"));
  core("runner", "tools", "Server", t("nav.myRunner"), wsHref("runner"), path.endsWith("/runner"), { inRail: false });
  if (wsSlug) {
    for (const item of extensionItems) {
      const href = `/w/${wsSlug}/x/${item.extensionId}`;
      items.push({
        id: `ext:${item.extensionId}`,
        group: "tools",
        icon: "Puzzle",
        label: item.title,
        href,
        active: path === href,
        origin: "extension",
        inRail: true,
      });
    }
    core("settings", "footer", "Settings", t("nav.workspaceSettings"), `/w/${wsSlug}/settings`, path === `/w/${wsSlug}/settings`, { inRail: false });
    if (projectSlug) {
      const href = `/w/${wsSlug}/p/${projectSlug}/settings`;
      core("project-settings", "footer", "Settings", t("nav.projectSettings"), href, path === href, { inRail: false });
    }
  }

  const toNavProject = (p: Project): NavProject => ({
    id: p.id,
    slug: p.slug,
    name: p.name,
    icon: p.icon,
    active: p.slug === projectSlug,
    href: `/w/${wsSlug}/p/${p.slug}`,
    items: [],
  });

  const retry = useCallback(() => {
    if (currentWorkspace) void fetchProjects(currentWorkspace.id);
  }, [currentWorkspace, fetchProjects]);

  let projectsState: Async<NavProject[]>;
  if (navigationPending) projectsState = { state: "loading" };
  else if (!currentWorkspace) projectsState = { state: "ready", value: [] };
  else if (projectsError) projectsState = { state: "error", error: { code: "projects", message: projectsError }, retry };
  else projectsState = { state: "ready", value: projects.map(toNavProject) };

  const data: NavSidebarData = {
    workspace: {
      id: currentWorkspace?.id ?? "",
      slug: currentWorkspace?.slug ?? "",
      name: currentWorkspace?.name ?? "",
      logoUrl: currentWorkspace?.icon_url ?? undefined,
    },
    workspaces: workspaces.map((w) => ({
      id: w.id,
      slug: w.slug,
      name: w.name,
      logoUrl: w.icon_url ?? undefined,
      href: `/w/${w.slug}`,
      current: w.slug === wsSlug,
    })),
    items,
    projects: projectsState,
    collapsed,
    isMobile: typeof window.matchMedia === "function" && window.matchMedia("(max-width: 767px)").matches,
    workspaceResolved: !!currentWorkspace,
    archivedProjects: archivedProjects.map(toNavProject),
    archivedError: unarchiveError,
  };

  const actions: NavSidebarActions = {
    toggleCollapsed: () => onToggle?.(),
    switchWorkspace: (slug) => navigate(`/w/${slug}`),
    createProject: () => setShowCreateProject(true),
    createWorkspace: () => setCreateWsOpen(true),
    unarchiveProject: async (id) => {
      setUnarchiveError(null);
      try {
        await unarchiveProject(id);
      } catch (err) {
        setUnarchiveError(apiErrorMessage(err, t("sidebar.projects.unarchiveFailed")));
      }
    },
  };

  const ctx = useBuildSlotContext(
    "nav.sidebar",
    currentWorkspace ? { id: currentWorkspace.id, slug: currentWorkspace.slug, name: currentWorkspace.name } : null,
  );
  const themeSettings = useThemeSettings();
  useSlotRegistryVersion();
  const View = withSlotFallback("nav.sidebar", resolveSlotView("nav.sidebar"));

  return (
    <>
      <View data={data} actions={actions} themeSettings={themeSettings} ctx={ctx} />

      <CreateProjectDialog
        open={showCreateProject}
        onOpenChange={setShowCreateProject}
      />

    {/* Create workspace dialog */}
    <Dialog open={createWsOpen} onOpenChange={(open) => { setCreateWsOpen(open); if (!open) { setWsName(""); setWsSlugDraft(""); setWsError(null); } }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("createWorkspace.title")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 pt-2">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("createWorkspace.name")}</label>
            <Input
              value={wsName}
              onChange={(e) => handleWsNameChange(e.target.value)}
              placeholder={t("createWorkspace.namePlaceholder")}
              autoFocus
              onKeyDown={(e) => { if (e.key === "Enter" && wsName.trim()) void handleCreateWs(); }}
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t("createWorkspace.slug")}</label>
            <Input
              value={wsSlugDraft}
              onChange={(e) => setWsSlugDraft(e.target.value)}
              placeholder={t("createWorkspace.slugPlaceholder")}
            />
            <p className="text-xs text-muted-foreground">{t("createWorkspace.slugHint", { slug: wsSlugDraft || "slug" })}</p>
          </div>
          {wsError && (
            <p className="text-sm text-destructive">{wsError}</p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setCreateWsOpen(false)}>{t("actions.cancel")}</Button>
            <Button size="sm" onClick={() => void handleCreateWs()} disabled={!wsName.trim() || !wsSlugDraft.trim() || wsCreating}>
              {wsCreating ? t("actions.creating") : t("actions.create")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
    </>
  );
}
