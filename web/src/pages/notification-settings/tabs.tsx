import { Bell, Mail, Monitor, Send } from "lucide-react";
import { cn } from "@/lib/cn";
import type { TabId } from "./constants";

const TABS: { id: TabId; label: string; Icon: typeof Bell }[] = [
  { id: "in-app", label: "In-App", Icon: Bell },
  { id: "push", label: "Browser Push", Icon: Monitor },
  { id: "email", label: "Email", Icon: Mail },
  { id: "telegram", label: "Telegram", Icon: Send },
];

interface NotificationSettingsTabsProps {
  activeTab: TabId;
  onChange: (tab: TabId) => void;
  emailUnavailable: boolean;
  telegramUnavailable: boolean;
}

export function NotificationSettingsTabs({
  activeTab,
  onChange,
  emailUnavailable,
  telegramUnavailable,
}: NotificationSettingsTabsProps) {
  return (
    <div className="flex items-center gap-0 border-b border-border" role="tablist">
      {TABS.map(({ id, label, Icon }) => {
        const isActive = activeTab === id;
        const unavailable =
          (id === "email" && emailUnavailable) ||
          (id === "telegram" && telegramUnavailable);
        return (
          <button
            key={id}
            role="tab"
            aria-selected={isActive}
            onClick={() => onChange(id)}
            className={cn(
              "flex h-10 items-center gap-1.5 border-b-2 px-3 text-sm transition-colors",
              isActive
                ? "border-primary font-medium text-foreground"
                : "border-transparent font-normal text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="h-3.5 w-3.5" />
            {label}
            {unavailable && (
              <span className="text-xs text-muted-foreground">(unavailable)</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
