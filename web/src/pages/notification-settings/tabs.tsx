import { Bell, Mail, Monitor, Send } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import type { TabId } from "./constants";

// `labelKey` resolves in the notifications namespace at render.
const TABS: { id: TabId; labelKey: string; Icon: typeof Bell }[] = [
  { id: "in-app", labelKey: "settings.tabs.inApp", Icon: Bell },
  { id: "push", labelKey: "settings.tabs.push", Icon: Monitor },
  { id: "email", labelKey: "settings.tabs.email", Icon: Mail },
  { id: "telegram", labelKey: "settings.tabs.telegram", Icon: Send },
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
  const { t } = useTranslation("notifications");
  return (
    <div className="flex items-center gap-0 border-b border-border" role="tablist">
      {TABS.map(({ id, labelKey, Icon }) => {
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
            {t(labelKey)}
            {unavailable && (
              <span className="text-xs text-muted-foreground">{t("settings.tabs.unavailable")}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
