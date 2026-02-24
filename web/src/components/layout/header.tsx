import { useCallback, useState } from "react";
import { Link, useParams } from "react-router";
import {
  ChevronRight,
  LogOut,
  Menu,
  Moon,
  Search,
  Sun,
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

interface HeaderProps {
  onToggleSidebar: () => void;
}

export function Header({ onToggleSidebar }: HeaderProps) {
  const { wsSlug, projectSlug } = useParams();
  const { user, logout } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();
  const { currentProject } = useProjectStore();
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
    <header className="flex h-14 items-center gap-4 border-b border-border bg-background px-4">
      <Button
        variant="ghost"
        size="icon"
        onClick={onToggleSidebar}
        className="shrink-0"
      >
        <Menu className="h-4 w-4" />
      </Button>

      {/* Breadcrumbs */}
      <nav className="flex items-center gap-1 text-sm text-muted-foreground">
        {currentWorkspace && (
          <>
            <Link
              to={`/w/${wsSlug}`}
              className="hover:text-foreground transition-colors"
            >
              {currentWorkspace.name}
            </Link>
            {currentProject && (
              <>
                <ChevronRight className="h-3 w-3" />
                <Link
                  to={`/w/${wsSlug}/p/${projectSlug}`}
                  className="hover:text-foreground transition-colors font-medium text-foreground"
                >
                  {currentProject.name}
                </Link>
              </>
            )}
          </>
        )}
      </nav>

      <div className="flex-1" />

      {/* Search */}
      <div className="relative hidden w-64 md:block">
        <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          placeholder="Search tasks..."
          className="pl-8"
        />
      </div>

      {/* Theme toggle */}
      <Button variant="ghost" size="icon" onClick={toggleTheme}>
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
