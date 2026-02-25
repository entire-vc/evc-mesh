import { useNavigate } from "react-router";
import { Columns3, GitBranch, List, Plus } from "lucide-react";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";

interface ViewTabBarProps {
  currentView: "board" | "list" | "timeline";
  wsSlug: string;
  projectSlug: string;
  className?: string;
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
] as const;

export function ViewTabBar({
  currentView,
  wsSlug,
  projectSlug,
  className,
}: ViewTabBarProps) {
  const navigate = useNavigate();

  return (
    <div
      className={cn(
        "flex items-center gap-0 border-b border-border",
        className,
      )}
    >
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
              "flex h-9 items-center gap-1.5 border-b-2 px-3 text-sm transition-colors",
              isActive
                ? "border-primary font-medium text-foreground"
                : "border-transparent font-normal text-muted-foreground hover:text-foreground",
            )}
            aria-current={isActive ? "page" : undefined}
          >
            <Icon className="h-3.5 w-3.5" />
            {label}
          </button>
        );
      })}

      {/* Divider + Add View placeholder */}
      <div className="ml-2 flex items-center border-l border-border pl-2">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1 px-2 text-xs text-muted-foreground hover:text-foreground"
          title="Add view (coming soon)"
          disabled
        >
          <Plus className="h-3 w-3" />
          View
        </Button>
      </div>
    </div>
  );
}
