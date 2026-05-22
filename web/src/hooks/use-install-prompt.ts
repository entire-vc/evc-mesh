import { useCallback, useEffect, useRef, useState } from "react";

const DISMISSED_KEY = "pwa_install_dismissed";
const DISMISS_TTL_MS = 30 * 24 * 60 * 60 * 1000;

type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
};

function isDismissed(): boolean {
  try {
    const raw = localStorage.getItem(DISMISSED_KEY);
    if (!raw) return false;
    return Date.now() - parseInt(raw, 10) < DISMISS_TTL_MS;
  } catch {
    return false;
  }
}

function isStandaloneMode(): boolean {
  return (
    window.matchMedia("(display-mode: standalone)").matches ||
    (navigator as unknown as { standalone?: boolean }).standalone === true
  );
}

export interface UseInstallPromptReturn {
  showBanner: boolean;
  showDesktopButton: boolean;
  promptInstall: () => Promise<void>;
  dismiss: () => void;
}

export function useInstallPrompt(): UseInstallPromptReturn {
  const [deferredPrompt, setDeferredPrompt] =
    useState<BeforeInstallPromptEvent | null>(null);
  const [engagementReady, setEngagementReady] = useState(false);
  const [dismissed, setDismissed] = useState(isDismissed);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const standalone = isStandaloneMode();

  useEffect(() => {
    if (standalone || dismissed) return;

    const handler = (e: Event) => {
      e.preventDefault();
      setDeferredPrompt(e as BeforeInstallPromptEvent);
    };
    window.addEventListener("beforeinstallprompt", handler);
    return () => window.removeEventListener("beforeinstallprompt", handler);
  }, [standalone, dismissed]);

  // 30s engagement timer — starts when prompt is first captured
  useEffect(() => {
    if (!deferredPrompt || engagementReady || dismissed) return;
    timerRef.current = setTimeout(() => setEngagementReady(true), 30_000);
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, [deferredPrompt, engagementReady, dismissed]);

  const promptInstall = useCallback(async () => {
    if (!deferredPrompt) return;
    await deferredPrompt.prompt();
    await deferredPrompt.userChoice;
    setDeferredPrompt(null);
    setEngagementReady(false);
  }, [deferredPrompt]);

  const dismiss = useCallback(() => {
    try {
      localStorage.setItem(DISMISSED_KEY, Date.now().toString());
    } catch {
      // ignore
    }
    setDismissed(true);
    setDeferredPrompt(null);
    setEngagementReady(false);
    if (timerRef.current) clearTimeout(timerRef.current);
  }, []);

  const canInstall = !standalone && !dismissed && deferredPrompt !== null;

  return {
    showBanner: canInstall && engagementReady,
    showDesktopButton: canInstall,
    promptInstall,
    dismiss,
  };
}
