import { useNavigate } from "react-router";
import { Calendar, Columns3, GitBranch, List, MoreVertical } from "lucide-react";
import { cn } from "@/lib/cn";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

interface ViewTabBarProps {
  currentView: "board" | "list" | "timeline" | "calendar";
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
  {
    id: "calendar" as const,
    label: "Calendar",
    Icon: Calendar,
    path: (ws: string, proj: string) => `/w/${ws}/p/${proj}/calendar`,
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

      {/* View options menu */}
      <div className="ml-1 flex items-center border-l border-border pl-1">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className="flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
              title="View options"
            >
              <MoreVertical className="h-3.5 w-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
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
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}
