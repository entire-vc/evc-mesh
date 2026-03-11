import { useEffect } from "react";
import { useNavigate } from "react-router";
import {
  Bookmark,
  BookmarkPlus,
  Calendar,
  Columns3,
  GitBranch,
  List,
  MoreVertical,
  Share2,
  Trash2,
} from "lucide-react";
import { cn } from "@/lib/cn";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useSavedViewStore } from "@/stores/saved-view-store";
import { toast } from "@/components/ui/toast";
import type { ViewType } from "@/types";

interface ViewTabBarProps {
  currentView: "board" | "list" | "timeline" | "calendar";
  wsSlug: string;
  projectSlug: string;
  projectId?: string;
  className?: string;
  /** Callback to open the "save current view" dialog from the page */
  onSaveView?: () => void;
}

const TABS = [
  {
    id: "board" as const,
    label: "Board",
    Icon: Columns3,
    path: (ws: string, proj: string) => `/w/${ws}/p/${proj}`,
  },
  {
    id: "list" as const,
    label: "List",
    Icon: List,
    path: (ws: string, proj: string) => `/w/${ws}/p/${proj}/list`,
  },
  {
    id: "timeline" as const,
    label: "Timeline",
    Icon: GitBranch,
    path: (ws: string, proj: string) => `/w/${ws}/p/${proj}/timeline`,
  },
  {
    id: "calendar" as const,
    label: "Calendar",
    Icon: Calendar,
    path: (ws: string, proj: string) => `/w/${ws}/p/${proj}/calendar`,
  },
] as const;

const VIEW_TYPE_PATH: Record<ViewType, (ws: string, proj: string) => string> = {
  board: (ws, proj) => `/w/${ws}/p/${proj}`,
  list: (ws, proj) => `/w/${ws}/p/${proj}/list`,
  timeline: (ws, proj) => `/w/${ws}/p/${proj}/timeline`,
  calendar: (ws, proj) => `/w/${ws}/p/${proj}/calendar`,
};

export function ViewTabBar({
  currentView,
  wsSlug,
  projectSlug,
  projectId,
  className,
  onSaveView,
}: ViewTabBarProps) {
  const navigate = useNavigate();
  const { views, fetchViews, applyView, updateView, deleteView } =
    useSavedViewStore();

  useEffect(() => {
    if (projectId) {
      fetchViews(projectId);
    }
  }, [projectId, fetchViews]);

  // Filter views for current view type
  const relevantViews = views.filter((v) => v.view_type === currentView);
  const personalViews = relevantViews.filter((v) => !v.is_shared);
  const sharedViews = relevantViews.filter((v) => v.is_shared);
  const hasViews = relevantViews.length > 0;

  const handleApply = (view: import("@/types").SavedView) => {
    // Navigate to the correct view type if needed
    const targetPath = VIEW_TYPE_PATH[view.view_type];
    if (targetPath && view.view_type !== currentView) {
      navigate(targetPath(wsSlug, projectSlug));
    }
    applyView(view);
    toast(`Applied view: ${view.name}`);
  };

  const handleToggleShare = async (view: import("@/types").SavedView) => {
    try {
      await updateView(view.id, { is_shared: !view.is_shared });
      toast(view.is_shared ? "View made private" : "View shared with team");
    } catch {
      toast("Failed to update view");
    }
  };

  const handleDelete = async (view: import("@/types").SavedView) => {
    try {
      await deleteView(view.id);
      toast("View deleted");
    } catch {
      toast("Failed to delete view");
    }
  };

  return (
    <div className={cn("flex items-center gap-0", className)}>
      {TABS.map(({ id, label, Icon, path }) => {
        const isActive = currentView === id;
        return (
          <button
            key={id}
            onClick={() => {
              if (!isActive) {
                navigate(path(wsSlug, projectSlug));
              }
            }}
            className={cn(
              "flex h-9 items-center gap-1.5 border-b-2 px-1.5 sm:px-3 text-sm transition-colors",
              isActive
                ? "border-primary font-medium text-foreground"
                : "border-transparent font-normal text-muted-foreground hover:text-foreground",
            )}
            aria-current={isActive ? "page" : undefined}
          >
            <Icon className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">{label}</span>
          </button>
        );
      })}

      {/* View options + Saved Views menu */}
      <div className="ml-1 flex items-center border-l border-border pl-1">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className="flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
              title="Views & options"
            >
              <MoreVertical className="h-3.5 w-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-56">
            {/* View type switcher */}
            {TABS.map(({ id, label, Icon, path }) => (
              <DropdownMenuItem
                key={id}
                onClick={() => navigate(path(wsSlug, projectSlug))}
                className={cn(currentView === id && "font-medium")}
              >
                <Icon className="mr-2 h-3.5 w-3.5" />
                {label}
              </DropdownMenuItem>
            ))}

            {/* Saved Views section */}
            {projectId && (
              <>
                <DropdownMenuSeparator />

                {onSaveView && (
                  <DropdownMenuItem onClick={onSaveView} className="gap-2">
                    <BookmarkPlus className="h-3.5 w-3.5" />
                    Save current view
                  </DropdownMenuItem>
                )}

                {personalViews.length > 0 && (
                  <>
                    <DropdownMenuLabel className="text-[10px] uppercase tracking-wide text-muted-foreground">
                      My views
                    </DropdownMenuLabel>
                    {personalViews.map((view) => (
                      <SavedViewRow
                        key={view.id}
                        view={view}
                        onApply={() => handleApply(view)}
                        onToggleShare={() => void handleToggleShare(view)}
                        onDelete={() => void handleDelete(view)}
                      />
                    ))}
                  </>
                )}

                {sharedViews.length > 0 && (
                  <>
                    <DropdownMenuLabel className="text-[10px] uppercase tracking-wide text-muted-foreground">
                      Shared views
                    </DropdownMenuLabel>
                    {sharedViews.map((view) => (
                      <SavedViewRow
                        key={view.id}
                        view={view}
                        onApply={() => handleApply(view)}
                        onToggleShare={() => void handleToggleShare(view)}
                        onDelete={() => void handleDelete(view)}
                      />
                    ))}
                  </>
                )}

                {!hasViews && !onSaveView && (
                  <div className="px-2 py-2 text-center text-xs text-muted-foreground">
                    No saved views
                  </div>
                )}
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Single saved view row
// ---------------------------------------------------------------------------

function SavedViewRow({
  view,
  onApply,
  onToggleShare,
  onDelete,
}: {
  view: import("@/types").SavedView;
  onApply: () => void;
  onToggleShare: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="group flex items-center justify-between rounded px-2 py-1.5 hover:bg-accent">
      <button
        className="flex flex-1 items-center gap-2 truncate text-left text-xs"
        onClick={onApply}
        title={view.name}
      >
        <Bookmark className="h-3 w-3 shrink-0 text-muted-foreground" />
        <span className="truncate">{view.name}</span>
      </button>
      <div className="hidden shrink-0 items-center gap-0.5 group-hover:flex">
        <button
          className="rounded p-0.5 hover:bg-muted"
          title={view.is_shared ? "Make private" : "Share with team"}
          onClick={(e) => {
            e.stopPropagation();
            onToggleShare();
          }}
        >
          <Share2
            className={cn(
              "h-3 w-3",
              view.is_shared ? "text-primary" : "text-muted-foreground",
            )}
          />
        </button>
        <button
          className="rounded p-0.5 hover:bg-muted"
          title="Delete view"
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 className="h-3 w-3 text-muted-foreground hover:text-destructive" />
        </button>
      </div>
    </div>
  );
}
