import { useEffect, useRef, useState } from 'react';
import { Bell, X } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { subscribeUser, getPermissionState } from '@/lib/push';

const DISMISS_KEY = 'push_prompt_dismissed';
const ACTIVE_SESSION_MS = 60_000;

function isDismissed(): boolean {
  try {
    const raw = localStorage.getItem(DISMISS_KEY);
    if (!raw) return false;
    const { dismissed_at } = JSON.parse(raw) as { dismissed_at: string };
    const sevenDaysMs = 7 * 24 * 60 * 60 * 1000;
    return Date.now() - new Date(dismissed_at).getTime() < sevenDaysMs;
  } catch {
    return false;
  }
}

function setDismissed() {
  localStorage.setItem(DISMISS_KEY, JSON.stringify({ dismissed_at: new Date().toISOString() }));
}

export function PushPermissionPrompt() {
  const [visible, setVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (typeof window === 'undefined' || !('Notification' in window)) return;
    if (isDismissed()) return;

    void (async () => {
      const perm = await getPermissionState();
      if (perm !== 'default') return;

      // Trigger after 60s of active session.
      timerRef.current = setTimeout(() => setVisible(true), ACTIVE_SESSION_MS);
    })();

    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  const handleEnable = async () => {
    setLoading(true);
    try {
      await subscribeUser();
    } catch {
      // user denied or error — dismiss banner
    } finally {
      setLoading(false);
      setDismissed();
      setVisible(false);
    }
  };

  const handleDismiss = () => {
    setDismissed();
    setVisible(false);
  };

  if (!visible) return null;

  return (
    <div className="fixed bottom-4 left-1/2 z-50 -translate-x-1/2 w-full max-w-md px-4">
      <div className="flex items-center gap-3 rounded-xl border border-border bg-background shadow-lg p-4">
        <Bell className="h-5 w-5 text-primary shrink-0" />
        <p className="flex-1 text-sm">
          Enable browser notifications for @mentions and assignments?
        </p>
        <div className="flex items-center gap-2 shrink-0">
          <Button size="sm" onClick={() => void handleEnable()} disabled={loading}>
            {loading ? 'Enabling…' : 'Enable'}
          </Button>
          <Button size="sm" variant="ghost" onClick={handleDismiss}>
            Not now
          </Button>
          <button onClick={handleDismiss} className="text-muted-foreground hover:text-foreground">
            <X className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  );
}
