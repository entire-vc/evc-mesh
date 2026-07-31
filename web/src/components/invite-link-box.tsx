import { useCallback, useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";

async function copyText(value: string) {
  try {
    await navigator.clipboard.writeText(value);
  } catch {
    // Fallback for browsers without the async clipboard API, and for any
    // non-secure origin — a self-hosted instance is often reached over plain
    // http, where navigator.clipboard is undefined.
    const textArea = document.createElement("textarea");
    textArea.value = value;
    document.body.appendChild(textArea);
    textArea.select();
    document.execCommand("copy");
    document.body.removeChild(textArea);
  }
}

interface InviteLinkBoxProps {
  url: string;
  /** Optional label shown above the link. */
  label?: string;
}

/**
 * Shows an invite link with a copy button.
 *
 * This is the delivery channel whenever no invitation email went out. Before
 * this existed the link was only ever written to the API log, so on an instance
 * without SMTP the only way to invite anyone was to read the container logs.
 */
export function InviteLinkBox({ url, label }: InviteLinkBoxProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    await copyText(url);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }, [url]);

  return (
    <div className="space-y-1.5">
      {label && <p className="text-xs font-medium">{label}</p>}
      <div className="flex items-center gap-2">
        <code className="flex-1 min-w-0 truncate rounded-md border border-border bg-muted px-2 py-1.5 text-xs">
          {url}
        </code>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 shrink-0"
          onClick={() => void handleCopy()}
        >
          {copied ? (
            <>
              <Check className="h-3.5 w-3.5" />
              Copied
            </>
          ) : (
            <>
              <Copy className="h-3.5 w-3.5" />
              Copy
            </>
          )}
        </Button>
      </div>
    </div>
  );
}
