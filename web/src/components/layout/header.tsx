import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSidebarItems } from "@/lib/extension-slots";
import { useTranslation } from "react-i18next";
import { useLocation, useNavigate, useParams } from "react-router";
import {
  ChevronRight,
  LayoutGrid,
  Loader2,
  LogOut,
  Network,
  Search as SearchIcon,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useNotificationStore } from "@/stores/notification";
import { Input } from "@/components/ui/input";
import { Avatar } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ViewTabBar } from "@/components/view-tab-bar";
import { projectTabsWith } from "@/lib/project-view-tabs";
import { useChatEnabled } from "@/lib/chat/use-chat-enabled";
import { NotificationBell } from "@/components/notification-bell";
import { api } from "@/lib/api";
import "@/core/slots/mesh-default";
import { resolveSlotView, useSlotRegistryVersion, useThemeSettings, withSlotFallback } from "@/core/slots/registry";
import { useBuildSlotContext } from "@/core/slots/use-slot-context";
import type { TopbarActions, TopbarCrumb, TopbarData } from "@/core/slots/nav-topbar/types";
import type { PaginatedResponse, ProjectViewTab, Task } from "@/types";

// ---------------------------------------------------------------------------
// TaskSearchResult — enriched result for display
// ---------------------------------------------------------------------------

interface TaskSearchResult {
  task: Task;
  projectName: string;
  projectSlug: string;
  statusColor: string;
}

// ---------------------------------------------------------------------------
// Search helpers
// ---------------------------------------------------------------------------

function extractUUIDFromURL(raw: string): string | null {
  const m = raw.match(
    /\/t(?:asks)?\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/i,
  );
  return m?.[1] ?? null;
}

function normalizeSearchQuery(raw: string): string {
  return raw.trim();
}

function isShortId(q: string): boolean {
  const s = q.startsWith("#") ? q.slice(1) : q;
  return /^[0-9a-f]{6,32}$/i.test(s);
}

// ---------------------------------------------------------------------------
// useTaskSearch — debounced cross-project search
// ---------------------------------------------------------------------------

function useTaskSearch() {
  const { currentProject, projects, statuses } = useProjectStore();
  const { currentWorkspace } = useWorkspaceStore();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<TaskSearchResult[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  // Build a status_id -> color map from currently loaded statuses (best-effort).
  // Statuses are loaded when visiting a project page, so this is populated at
  // least for the currently active project.
  const statusColorMap = useCallback((): Map<string, string> => {
    const m = new Map<string, string>();
    for (const s of statuses) {
      m.set(s.id, s.color);
    }
    return m;
  }, [statuses]);

  const search = useCallback(
    async (q: string) => {
      if (!q.trim() || q.trim().length < 2) {
        setResults([]);
        setIsLoading(false);
        return;
      }

      // Cancel in-flight fetch
      if (abortRef.current) {
        abortRef.current.abort();
      }
      abortRef.current = new AbortController();

      setIsLoading(true);

      // Short-id fast path: bypass project scope, search workspace-wide so a
      // task from any project is found regardless of which project is open.
      if (isShortId(q) && currentWorkspace?.id) {
        try {
          const page = await api<PaginatedResponse<Task>>(
            `/api/v1/workspaces/${currentWorkspace.id}/tasks`,
            { params: { search: q, page_size: "10" } },
          );
          const colorMap = statusColorMap();
          const enriched: TaskSearchResult[] = (page.items ?? [])
            .slice(0, 8)
            .map((task) => {
              const proj = projects.find((p) => p.id === task.project_id);
              return {
                task,
                projectName: proj?.name ?? "—",
                projectSlug: proj?.slug ?? "",
                statusColor: colorMap.get(task.status_id) ?? "var(--fallback-gray-500)",
              };
            });
          setResults(enriched);
        } catch {
          setResults([]);
        } finally {
          setIsLoading(false);
        }
        return;
      }

      try {
        // Determine which projects to search.
        const searchProjects = currentProject
          ? [currentProject]
          : projects.slice(0, 10); // cap fan-out

        if (searchProjects.length === 0) {
          setResults([]);
          setIsLoading(false);
          return;
        }

        const searchParams = { search: q, page_size: "20" };
        const colorMap = statusColorMap();

        const settled = await Promise.allSettled(
          searchProjects.map((proj) =>
            api<PaginatedResponse<Task>>(
              `/api/v1/projects/${proj.id}/tasks`,
              { params: searchParams },
            ).then((page) => ({ proj, items: page.items ?? [] })),
          ),
        );

        const enriched: TaskSearchResult[] = [];

        for (const result of settled) {
          if (result.status !== "fulfilled") continue;
          const { proj, items } = result.value;

          for (const task of items) {
            if (enriched.length >= 8) break;
            enriched.push({
              task,
              projectName: proj.name,
              projectSlug: proj.slug,
              // Use cached status color when available; fall back to neutral gray.
              statusColor: colorMap.get(task.status_id) ?? "var(--fallback-gray-500)",
            });
          }

          if (enriched.length >= 8) break;
        }

        setResults(enriched.slice(0, 8));
      } catch {
        // Ignore aborted requests and other errors silently
      } finally {
        setIsLoading(false);
      }
    },
    [currentProject, currentWorkspace, projects, statusColorMap],
  );

  const handleQueryChange = useCallback(
    (value: string) => {
      setQuery(value);

      if (debounceRef.current) {
        clearTimeout(debounceRef.current);
      }

      if (!value.trim() || value.trim().length < 2) {
        setResults([]);
        setIsLoading(false);
        return;
      }

      setIsLoading(true);
      debounceRef.current = setTimeout(() => {
        void search(value);
      }, 300);
    },
    [search],
  );

  const clear = useCallback(() => {
    setQuery("");
    setResults([]);
    setIsLoading(false);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (abortRef.current) abortRef.current.abort();
  }, []);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
      if (abortRef.current) abortRef.current.abort();
    };
  }, []);

  return { query, handleQueryChange, results, isLoading, clear };
}

// ---------------------------------------------------------------------------
// TaskSearchBox — the actual search input + dropdown
// ---------------------------------------------------------------------------

function TaskSearchBox({ wsSlug }: { wsSlug: string | undefined }) {
  const { t } = useTranslation("shell");
  const navigate = useNavigate();
  const { query, handleQueryChange, results, isLoading, clear } =
    useTaskSearch();
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [activeIndex, setActiveIndex] = useState(-1);

  const showDropdown = open && query.trim().length >= 2;

  // Close on outside click
  useEffect(() => {
    const handleClick = (e: MouseEvent) => {
      if (
        containerRef.current &&
        !containerRef.current.contains(e.target as Node)
      ) {
        setOpen(false);
        setActiveIndex(-1);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, []);

  // Reset active index when results change
  useEffect(() => {
    setActiveIndex(-1);
  }, [results]);

  const handleSelect = useCallback(
    (result: TaskSearchResult) => {
      navigate(`/w/${wsSlug}/p/${result.projectSlug}/t/${result.task.id}`);
      clear();
      setOpen(false);
      setActiveIndex(-1);
      inputRef.current?.blur();
    },
    [navigate, wsSlug, clear],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (!showDropdown) return;

      if (e.key === "Escape") {
        setOpen(false);
        setActiveIndex(-1);
        inputRef.current?.blur();
        return;
      }

      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveIndex((i) => Math.min(i + 1, results.length - 1));
        return;
      }

      if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveIndex((i) => Math.max(i - 1, 0));
        return;
      }

      if (e.key === "Enter" && activeIndex >= 0 && results[activeIndex]) {
        e.preventDefault();
        handleSelect(results[activeIndex]);
        return;
      }
    },
    [showDropdown, results, activeIndex, handleSelect],
  );

  return (
    <div ref={containerRef} className="relative hidden w-64 md:block">
      <SearchIcon className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground pointer-events-none" />
      {isLoading && (
        <Loader2 className="absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground animate-spin pointer-events-none" />
      )}
      {/* No type="search": it adds 3 DOM mutations to view.switch (perf budget). */}
      <Input
        ref={inputRef}
        name="mesh-task-search"
        data-1p-ignore
        data-bwignore
        placeholder={t("header.search.placeholder")}
        className="pl-8 pr-8"
        value={query}
        onChange={(e) => {
          const uuid = extractUUIDFromURL(e.target.value);
          if (uuid) {
            navigate(`/t/${uuid}`);
            clear();
            setOpen(false);
            return;
          }
          handleQueryChange(normalizeSearchQuery(e.target.value));
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={handleKeyDown}
        autoComplete="off"
        spellCheck={false}
        role="combobox"
        aria-expanded={showDropdown}
        aria-autocomplete="list"
      />

      {showDropdown && (
        <div
          className="absolute left-0 right-0 top-full z-50 mt-1 overflow-hidden rounded-lg border border-border bg-popover shadow-lg"
          role="listbox"
        >
          {isLoading && results.length === 0 ? (
            <div className="flex items-center justify-center gap-2 px-3 py-4 text-sm text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              {t("header.search.searching")}
            </div>
          ) : results.length === 0 ? (
            <div className="px-3 py-4 text-center text-sm text-muted-foreground">
              {t("header.search.noResults")}
            </div>
          ) : (
            <ul className="max-h-80 overflow-y-auto py-1">
              {results.map((result, idx) => (
                <li
                  key={result.task.id}
                  role="option"
                  aria-selected={idx === activeIndex}
                  className={`flex cursor-pointer items-start gap-2.5 px-3 py-2 text-sm transition-colors ${
                    idx === activeIndex
                      ? "bg-accent text-accent-foreground"
                      : "hover:bg-accent hover:text-accent-foreground"
                  }`}
                  onMouseDown={(e) => {
                    // Prevent blur on input before click fires
                    e.preventDefault();
                  }}
                  onClick={() => handleSelect(result)}
                  onMouseEnter={() => setActiveIndex(idx)}
                >
                  {/* Status color dot */}
                  <span
                    className="mt-0.5 h-2 w-2 shrink-0 rounded-full"
                    style={{ backgroundColor: result.statusColor }}
                  />
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-medium leading-tight">
                      {result.task.title}
                    </p>
                    <p className="truncate text-xs text-muted-foreground">
                      {result.projectName}
                    </p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// useOpenTaskProjectLink — view-strip target for the task's OWN project
// ---------------------------------------------------------------------------

// A task can be opened while a *different* project is selected in the
// sidebar (direct link into someone else's project, cross-project search
// result, etc.) — currentProject/:projectSlug lag or disagree in that case.
// Resolve the docs target from the open task's own project_id instead of
// trusting the URL or the sidebar selection, so the Docs tab always points at
// the docs that actually belong to what's on screen.
function useOpenTaskProjectLink(): {
  wsSlug: string;
  projectSlug: string;
  projectId: string;
} | null {
  const { wsSlug, taskId } = useParams();
  const { projects } = useProjectStore();
  const openTask = useTaskStore((state) =>
    taskId ? state.tasksById[taskId] ?? null : null,
  );

  if (!wsSlug || !taskId || !openTask) return null;
  const project = projects.find((p) => p.id === openTask.project_id);
  if (!project) return null;
  return { wsSlug, projectSlug: project.slug, projectId: project.id };
}

// ---------------------------------------------------------------------------
// HeaderProps
// ---------------------------------------------------------------------------

interface HeaderProps {
  onToggleSidebar: () => void;
  installable?: boolean;
  onInstall?: () => void;
}

// Also gates whether the view strip renders at all: a project route missing
// from here gets no tabs.
function useCurrentView(): ProjectViewTab | null {
  const location = useLocation();
  const path = location.pathname;
  // Before the suffix checks: an extension id like "calendar" ends the same way.
  const ext = /\/p\/[^/]+\/x\/([^/]+)\/?$/.exec(path);
  if (ext) return `ext:${ext[1]}`;
  const suffix = /\/(list|timeline|calendar|updates)\/?$/.exec(path);
  if (suffix) return suffix[1] as ProjectViewTab;
  // /docs and /docs/:docId are the same tab — matching only the bare path left
  // an open document with no tab highlighted and no view strip at all. Chat
  // likewise carries a conversation id.
  const section = /\/(chat|docs)(\/[^/]+)?\/?$/.exec(path);
  if (section) return section[1] as ProjectViewTab;
  // Check if we're on a project page (board is the default project view)
  if (/\/w\/[^/]+\/p\/[^/]+\/?$/.test(path)) return "board";
  return null;
}

function useTeamView(): "tree" | "grid" | null {
  const location = useLocation();
  const path = location.pathname;
  if (/\/w\/[^/]+\/org-chart\/grid\/?$/.test(path)) return "grid";
  if (/\/w\/[^/]+\/org-chart\/?$/.test(path)) return "tree";
  return null;
}

interface WorkspacePage {
  /** `shell` namespace key, resolved at render. */
  titleKey: string;
  /** Icon name from the nav slots' icon set. */
  icon: string;
}

const WORKSPACE_PAGES: Array<{
  pattern: RegExp;
  titleKey: string;
  icon: string;
}> = [
  { pattern: /\/w\/[^/]+\/?$/, titleKey: "nav.dashboard", icon: "LayoutDashboard" },
  { pattern: /\/w\/[^/]+\/org-chart(\/grid)?\/?$/, titleKey: "nav.team", icon: "Bot" },
  { pattern: /\/w\/[^/]+\/memories\/?$/, titleKey: "nav.memory", icon: "Brain" },
  { pattern: /\/w\/[^/]+\/sessions\/?$/, titleKey: "nav.sessions", icon: "MonitorDot" },
  { pattern: /\/w\/[^/]+\/spark\/?$/, titleKey: "nav.spark", icon: "Sparkles" },
  { pattern: /\/w\/[^/]+\/events\/?$/, titleKey: "nav.events", icon: "Activity" },
  { pattern: /\/w\/[^/]+\/analytics\/?$/, titleKey: "nav.analytics", icon: "BarChart2" },
  { pattern: /\/w\/[^/]+\/extensions\/?$/, titleKey: "nav.extensions", icon: "Settings" },
  { pattern: /\/w\/[^/]+\/initiatives\/?$/, titleKey: "nav.initiatives", icon: "Target" },
  { pattern: /\/w\/[^/]+\/triage\/?$/, titleKey: "nav.triage", icon: "Inbox" },
  { pattern: /\/w\/[^/]+\/me\/?$/, titleKey: "nav.profile", icon: "UserRound" },
];

function useWorkspacePage(): WorkspacePage | null {
  const location = useLocation();
  const path = location.pathname;
  // Only match workspace-level pages (not project pages)
  if (/\/w\/[^/]+\/p\//.test(path)) return null;
  for (const entry of WORKSPACE_PAGES) {
    if (entry.pattern.test(path)) {
      return { titleKey: entry.titleKey, icon: entry.icon };
    }
  }
  return null;
}

const TEAM_TABS = [
  {
    id: "tree" as const,
    labelKey: "header.teamTabs.tree",
    Icon: Network,
    path: (ws: string) => `/w/${ws}/org-chart`,
  },
  {
    id: "grid" as const,
    labelKey: "header.teamTabs.byProject",
    Icon: LayoutGrid,
    path: (ws: string) => `/w/${ws}/org-chart/grid`,
  },
] as const;

function TeamViewTabs({ currentView, wsSlug }: { currentView: "tree" | "grid"; wsSlug: string }) {
  const navigate = useNavigate();
  const { t } = useTranslation("shell");
  return (
    <div className="flex items-center gap-0 ml-1 sm:ml-4">
      {TEAM_TABS.map(({ id, labelKey, Icon, path }) => {
        const isActive = currentView === id;
        return (
          <button
            key={id}
            onClick={() => { if (!isActive) navigate(path(wsSlug)); }}
            className={cn(
              "flex h-9 items-center gap-1.5 border-b-2 px-1.5 sm:px-3 text-sm transition-colors",
              isActive
                ? "border-primary font-medium text-foreground"
                : "border-transparent font-normal text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">{t(labelKey)}</span>
          </button>
        );
      })}
    </div>
  );
}

// The blocks the header hands to its slot view: each reads its own state, so the view only
// decides where they go. Module-level, so their identity (and the state under them) is stable.
function ViewTabs() {
  const { wsSlug, projectSlug } = useParams();
  const { currentProject } = useProjectStore();
  const currentView = useCurrentView();
  const openTaskProject = useOpenTaskProjectLink();
  const sidebarItems = useSidebarItems();
  const chatEnabled = useChatEnabled(currentProject?.id);
  // Built-ins, the Chat tab where the project turned it on, one tab per enabled
  // extension screen; stable while the registry is.
  const tabs = useMemo(() => projectTabsWith(sidebarItems, chatEnabled), [sidebarItems, chatEnabled]);
  // View tabs — shown when inside a project. On an open task the same
  // strip stays (no tab active) so Docs is a tab, not a separate icon;
  // it targets the task's own project (see useOpenTaskProjectLink).
  if (currentView && wsSlug && projectSlug) {
    return (
      <ViewTabBar
        currentView={currentView}
        wsSlug={wsSlug}
        projectSlug={projectSlug}
        projectId={currentProject?.id}
        tabs={tabs}
        className="mesh-project-tabs ml-1 shrink-0 sm:ml-4"
      />
    );
  }
  return openTaskProject ? (
    <ViewTabBar
      currentView={null}
      wsSlug={openTaskProject.wsSlug}
      projectSlug={openTaskProject.projectSlug}
      projectId={openTaskProject.projectId}
      tabs={tabs}
      className="mesh-project-tabs ml-1 shrink-0 sm:ml-4"
    />
  ) : null;
}

// Team view tabs — shown on the org-chart page.
function TeamTabs() {
  const { wsSlug } = useParams();
  const teamView = useTeamView();
  return teamView && wsSlug ? <TeamViewTabs currentView={teamView} wsSlug={wsSlug} /> : null;
}

function Search() {
  const { wsSlug } = useParams();
  return <TaskSearchBox wsSlug={wsSlug} />;
}

// User menu — name and email open the profile screen, where theme,
// language and app access live.
function UserMenu() {
  const { t } = useTranslation("shell");
  const { wsSlug } = useParams();
  const navigate = useNavigate();
  const { user, logout } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();
  const profileSlug = wsSlug ?? currentWorkspace?.slug;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger>
        <Avatar name={user?.name || t("header.userMenu.userFallback")} src={user?.avatar_url} size="sm" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[14rem]">
        {profileSlug ? (
          <DropdownMenuItem
            aria-label={t("header.userMenu.profile")}
            onClick={() => navigate(`/w/${profileSlug}/me`)}
            className="gap-3 py-1.5"
          >
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm font-medium">{user?.name}</span>
              <span className="block truncate text-xs text-muted-foreground">{user?.email}</span>
            </span>
            <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
          </DropdownMenuItem>
        ) : (
          <div className="px-2 py-1.5">
            <p className="text-sm font-medium">{user?.name}</p>
            <p className="text-xs text-muted-foreground">{user?.email}</p>
          </div>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={logout}>
          <LogOut className="mr-2 h-4 w-4" />
          {t("header.userMenu.logOut")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

const TOPBAR_COMPONENTS: TopbarData["components"] = {
  NotificationBell,
  UserMenu,
  ViewTabs,
  TeamTabs,
  Search,
};

// The container of the `nav.topbar` slot: route, stores and the core blocks live here; what is
// drawn lives in the active theme's view (`core/slots/nav-topbar/mesh-default-view.tsx`).
export function Header({ onToggleSidebar, installable, onInstall }: HeaderProps) {
  const { t } = useTranslation("shell");
  const { wsSlug, projectSlug } = useParams();
  const { user } = useAuthStore();
  const unread = useNotificationStore((s) => s.unreadCount);
  const { currentWorkspace } = useWorkspaceStore();
  const { currentProject } = useProjectStore();
  const workspacePage = useWorkspacePage();

  const breadcrumbs: TopbarCrumb[] = [];
  if (currentWorkspace) {
    breadcrumbs.push({ kind: "workspace", label: currentWorkspace.name, href: `/w/${wsSlug}` });
    if (currentProject) {
      breadcrumbs.push({ kind: "project", label: currentProject.name, href: `/w/${wsSlug}/p/${projectSlug}`, strong: true });
    } else if (workspacePage) {
      breadcrumbs.push({ kind: "page", label: t(workspacePage.titleKey), icon: workspacePage.icon, strong: true });
    }
  }

  const data: TopbarData = {
    breadcrumbs,
    user: { id: user?.id ?? "", name: user?.name ?? "", avatarUrl: user?.avatar_url || undefined },
    notifications: { unread },
    installable: !!(installable && onInstall),
    isMobile: typeof window.matchMedia === "function" && window.matchMedia("(max-width: 767px)").matches,
    components: TOPBAR_COMPONENTS,
  };
  const actions: TopbarActions = {
    toggleSidebar: onToggleSidebar,
    install: installable && onInstall ? onInstall : undefined,
  };
  const ctx = useBuildSlotContext(
    "nav.topbar",
    currentWorkspace ? { id: currentWorkspace.id, slug: currentWorkspace.slug, name: currentWorkspace.name } : null,
  );
  const themeSettings = useThemeSettings();
  useSlotRegistryVersion();
  const View = withSlotFallback("nav.topbar", resolveSlotView("nav.topbar"));
  return <View data={data} actions={actions} themeSettings={themeSettings} ctx={ctx} />;
}
