// Icon names the nav slots speak (lucide, core set). A view maps the name to a
// component here or draws its own; an unknown name falls back to the puzzle piece.
import {
  Activity,
  BarChart2,
  Bell,
  Bot,
  Brain,
  FolderKanban,
  Inbox,
  LayoutDashboard,
  MonitorDot,
  Plug,
  Puzzle,
  Server,
  Settings,
  Sparkles,
  Target,
  UserRound,
} from "lucide-react";
import type { ComponentType } from "react";

export type NavIcon = ComponentType<{ className?: string }>;

export const NAV_ICONS: Record<string, NavIcon> = {
  Activity,
  BarChart2,
  Bell,
  Bot,
  Brain,
  FolderKanban,
  Inbox,
  LayoutDashboard,
  MonitorDot,
  Plug,
  Puzzle,
  Server,
  Settings,
  Sparkles,
  Target,
  UserRound,
};

export function navIcon(name: string): NavIcon {
  return NAV_ICONS[name] ?? Puzzle;
}
