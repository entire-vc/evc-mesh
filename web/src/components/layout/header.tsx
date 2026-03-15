import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import {
  Activity,
  BarChart2,
  Bot,
  Brain,
  ChevronRight,
  Inbox,
  LayoutDashboard,
  Loader2,
  LogOut,
  Menu,
  MonitorDot,
  Moon,
  Search,
  Settings,
  Sparkles,
  Sun,
  Target,
} from "lucide-react";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { Button } from "@/components/ui/button";
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
import { NotificationBell } from "@/components/notification-bell";
import { api } from "@/lib/api";
import type { PaginatedResponse, Task } from "@/types";

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
// useTaskSearch — debounced cross-project search
// ---------------------------------------------------------------------------

function useTaskSearch() {
  const { currentProject, projects, statuses } = useProjectStore();
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
              statusColor: colorMap.get(task.status_id) ?? "#6b7280",
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
    [currentProject, projects, statusColorMap],
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
      <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground pointer-events-none" />
      {isLoading && (
        <Loader2 className="absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground animate-spin pointer-events-none" />
      )}
      <Input
        ref={inputRef}
        placeholder="Search tasks..."
        className="pl-8 pr-8"
        value={query}
        onChange={(e) => {
          handleQueryChange(e.target.value);
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
              Searching...
            </div>
          ) : results.length === 0 ? (
            <div className="px-3 py-4 text-center text-sm text-muted-foreground">
              No results found
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
// HeaderProps
// ---------------------------------------------------------------------------

interface HeaderProps {
  onToggleSidebar: () => void;
}

function useCurrentView(): "board" | "list" | "timeline" | "calendar" | null {
  const location = useLocation();
  const path = location.pathname;
  if (path.endsWith("/list")) return "list";
  if (path.endsWith("/timeline")) return "timeline";
  if (path.endsWith("/calendar")) return "calendar";
  // Check if we're on a project page (board is the default project view)
  if (/\/w\/[^/]+\/p\/[^/]+\/?$/.test(path)) return "board";
  return null;
}

interface WorkspacePage {
  title: string;
  icon: React.ComponentType<{ className?: string }>;
}

const WORKSPACE_PAGES: Array<{
  pattern: RegExp;
  title: string;
  icon: React.ComponentType<{ className?: string }>;
}> = [
  { pattern: /\/w\/[^/]+\/?$/, title: "Dashboard", icon: LayoutDashboard },
  { pattern: /\/w\/[^/]+\/org-chart\/?$/, title: "Team", icon: Bot },
  { pattern: /\/w\/[^/]+\/memories\/?$/, title: "Memory", icon: Brain },
  { pattern: /\/w\/[^/]+\/sessions\/?$/, title: "Sessions", icon: MonitorDot },
  { pattern: /\/w\/[^/]+\/spark\/?$/, title: "Spark Catalog", icon: Sparkles },
  { pattern: /\/w\/[^/]+\/events\/?$/, title: "Events", icon: Activity },
  { pattern: /\/w\/[^/]+\/analytics\/?$/, title: "Analytics", icon: BarChart2 },
  { pattern: /\/w\/[^/]+\/integrations\/?$/, title: "Integrations", icon: Settings },
  { pattern: /\/w\/[^/]+\/initiatives\/?$/, title: "Initiatives", icon: Target },
  { pattern: /\/w\/[^/]+\/triage\/?$/, title: "Triage Inbox", icon: Inbox },
];

function useWorkspacePage(): WorkspacePage | null {
  const location = useLocation();
  const path = location.pathname;
  // Only match workspace-level pages (not project pages)
  if (/\/w\/[^/]+\/p\//.test(path)) return null;
  for (const entry of WORKSPACE_PAGES) {
    if (entry.pattern.test(path)) {
      return { title: entry.title, icon: entry.icon };
    }
  }
  return null;
}

export function Header({ onToggleSidebar }: HeaderProps) {
  const { wsSlug, projectSlug } = useParams();
  const { user, logout } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();
  const { currentProject } = useProjectStore();
  const currentView = useCurrentView();
  const workspacePage = useWorkspacePage();
  const [isDark, setIsDark] = useState(
    document.documentElement.classList.contains("dark"),
  );

  const toggleTheme = useCallback(() => {
    const next = !isDark;
    setIsDark(next);
    document.documentElement.classList.toggle("dark", next);
    localStorage.setItem("theme", next ? "dark" : "light");
  }, [isDark]);

  return (
    <header className="flex h-14 items-center gap-2 sm:gap-4 border-b border-border bg-background px-2 sm:px-4">
      <Button
        variant="ghost"
        size="icon"
        onClick={onToggleSidebar}
        className="shrink-0"
      >
        <Menu className="h-4 w-4" />
      </Button>

      {/* Breadcrumbs */}
      <nav className="flex min-w-0 items-center gap-1 text-sm text-muted-foreground">
        {currentWorkspace && (
          <>
            <Link
              to={`/w/${wsSlug}`}
              className="hover:text-foreground transition-colors"
            >
              <span className="truncate max-w-[80px] sm:max-w-[160px]">{currentWorkspace.name}</span>
            </Link>
            {currentProject && (
              <>
                <ChevronRight className="h-3 w-3" />
                <Link
                  to={`/w/${wsSlug}/p/${projectSlug}`}
                  className="hover:text-foreground transition-colors font-medium text-foreground"
                >
                  <span className="truncate max-w-[100px] sm:max-w-none">{currentProject.name}</span>
                </Link>
              </>
            )}
            {!currentProject && workspacePage && (
              <>
                <ChevronRight className="h-3 w-3" />
                <span className="flex items-center gap-1.5 font-medium text-foreground">
                  <workspacePage.icon className="h-3.5 w-3.5" />
                  {workspacePage.title}
                </span>
              </>
            )}
          </>
        )}
      </nav>

      {/* View tabs — shown when inside a project */}
      {currentView && wsSlug && projectSlug && (
        <ViewTabBar
          currentView={currentView}
          wsSlug={wsSlug}
          projectSlug={projectSlug}
          projectId={currentProject?.id}
          className="ml-1 sm:ml-4"
        />
      )}

      <div className="flex-1" />

      {/* Search */}
      <TaskSearchBox wsSlug={wsSlug} />

      {/* Notifications */}
      <NotificationBell />

      {/* Theme toggle */}
      <Button variant="ghost" size="icon" onClick={toggleTheme} className="hidden sm:inline-flex">
        {isDark ? (
          <Sun className="h-4 w-4" />
        ) : (
          <Moon className="h-4 w-4" />
        )}
      </Button>

      {/* User menu */}
      <DropdownMenu>
        <DropdownMenuTrigger>
          <Avatar name={user?.name || "User"} src={user?.avatar_url} size="sm" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <div className="px-2 py-1.5">
            <p className="text-sm font-medium">{user?.name}</p>
            <p className="text-xs text-muted-foreground">{user?.email}</p>
          </div>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={logout}>
            <LogOut className="mr-2 h-4 w-4" />
            Log out
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </header>
  );
}
