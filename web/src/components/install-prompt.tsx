import { useTranslation } from "react-i18next";
import { Download, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { UseInstallPromptReturn } from "@/hooks/use-install-prompt";

// ---------------------------------------------------------------------------
// InstallPromptBanner — mobile bottom banner, shown after 30s engagement
// ---------------------------------------------------------------------------

interface BannerProps {
  onInstall: () => void;
  onDismiss: () => void;
}

export function InstallPromptBanner({ onInstall, onDismiss }: BannerProps) {
  const { t } = useTranslation("settings");
  return (
    <div
      role="banner"
      aria-label={t("install.bannerLabel")}
      className="fixed bottom-0 left-0 right-0 z-50 flex items-center gap-3 border-t border-border bg-background px-4 py-3 shadow-lg md:hidden"
    >
      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10">
        <Download className="h-4 w-4 text-primary" />
      </div>
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium leading-tight">{t("install.title")}</p>
        <p className="text-xs text-muted-foreground">{t("install.addToHome")}</p>
      </div>
      <Button size="sm" onClick={onInstall} className="shrink-0">
        {t("install.install")}
      </Button>
      <Button
        variant="ghost"
        size="icon"
        onClick={onDismiss}
        className="shrink-0 h-8 w-8"
        aria-label={t("install.dismiss")}
      >
        <X className="h-4 w-4" />
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// InstallPromptButton — desktop header icon, subtle and non-intrusive
// ---------------------------------------------------------------------------

interface ButtonProps {
  onInstall: () => void;
}

export function InstallPromptButton({ onInstall }: ButtonProps) {
  const { t } = useTranslation("settings");
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={onInstall}
      title={t("install.asApp")}
      className="hidden md:inline-flex"
    >
      <Download className="h-4 w-4" />
    </Button>
  );
}

// ---------------------------------------------------------------------------
// InstallPromptRoot — combines both; renders correct variant based on context
// ---------------------------------------------------------------------------

export function InstallPromptRoot({
  showBanner,
  showDesktopButton,
  promptInstall,
  dismiss,
}: UseInstallPromptReturn) {
  return (
    <>
      {showBanner && (
        <InstallPromptBanner onInstall={promptInstall} onDismiss={dismiss} />
      )}
      {showDesktopButton && !showBanner && (
        <InstallPromptButton onInstall={promptInstall} />
      )}
    </>
  );
}
