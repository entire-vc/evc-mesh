import { useCallback, useState } from "react";
import { AlertTriangle, Check, Copy } from "lucide-react";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";
import { DialogFooter } from "@/components/ui/dialog";

interface ApiKeyRevealPanelProps {
  apiKey: string;
  onClose: () => void;
}

/**
 * The "here is your new key, you will not see it again" body — key box,
 * copy button, and the yellow one-time warning — plus the footer Close/Done
 * button. Shared by register-agent-dialog.tsx, agent-detail-dialog.tsx's
 * regenerate-key mode, and the U4 invite-agent-to-workspace dialog: all
 * three previously carried an independent copy of this exact markup, which
 * meant the "shown once" wording could drift between them without anyone
 * noticing (task U4).
 *
 * Deliberately renders only the body + footer, not the DialogHeader — each
 * caller's title/description differs ("Agent Registered Successfully" vs
 * "New API Key Generated" vs an invite-specific phrasing) and stays with
 * the caller.
 *
 * There is and can be no "reveal key" button anywhere in this app: the
 * server stores a bcrypt hash, not the key, so a caller past this one
 * render has nothing left to show.
 */
export function ApiKeyRevealPanel({ apiKey, onClose }: ApiKeyRevealPanelProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(apiKey);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Fallback for older browsers
      const textArea = document.createElement("textarea");
      textArea.value = apiKey;
      document.body.appendChild(textArea);
      textArea.select();
      document.execCommand("copy");
      document.body.removeChild(textArea);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [apiKey]);

  return (
    <>
      <div className="mt-4 space-y-4">
        <div className="rounded-lg border border-border bg-muted p-4">
          <p className="mb-2 text-xs font-medium text-muted-foreground">
            API Key
          </p>
          <div className="flex items-center gap-2">
            <code className="flex-1 break-all font-mono text-sm">
              {apiKey}
            </code>
            <Button
              type="button"
              variant="outline"
              size="icon"
              onClick={() => void handleCopy()}
              className="shrink-0"
            >
              {copied ? (
                <Check className="h-4 w-4 text-green-500" />
              ) : (
                <Copy className="h-4 w-4" />
              )}
            </Button>
          </div>
        </div>

        <div
          className={cn(
            "flex items-start gap-2 rounded-lg border border-yellow-200 bg-yellow-50 p-3",
            "dark:border-yellow-900 dark:bg-yellow-950",
          )}
        >
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-yellow-600" />
          <p className="text-sm text-yellow-800 dark:text-yellow-200">
            This key will only be shown once. Store it securely. You will not
            be able to retrieve it later.
          </p>
        </div>
      </div>

      <DialogFooter>
        <Button onClick={onClose}>{copied ? "Done" : "Close"}</Button>
      </DialogFooter>
    </>
  );
}
