import { useCallback, useEffect, useRef, useState } from "react";
import { Plus, X } from "lucide-react";
import { cn } from "@/lib/cn";

interface LabelEditorProps {
  labels: string[];
  onChange: (labels: string[]) => void;
  className?: string;
}

export function LabelEditor({ labels, onChange, className }: LabelEditorProps) {
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  // Auto-focus when the inline input appears.
  useEffect(() => {
    if (adding && inputRef.current) {
      inputRef.current.focus();
    }
  }, [adding]);

  const commitDraft = useCallback(
    (raw: string) => {
      const trimmed = raw.trim().toLowerCase();
      if (trimmed && !labels.includes(trimmed)) {
        onChange([...labels, trimmed]);
      }
      setDraft("");
      setAdding(false);
    },
    [labels, onChange],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (e.key === "Enter") {
        e.preventDefault();
        commitDraft(draft);
      } else if (e.key === "Escape") {
        setDraft("");
        setAdding(false);
      }
    },
    [draft, commitDraft],
  );

  const handleBlur = useCallback(() => {
    commitDraft(draft);
  }, [draft, commitDraft]);

  const removeLabel = useCallback(
    (label: string) => {
      onChange(labels.filter((l) => l !== label));
    },
    [labels, onChange],
  );

  return (
    <div className={cn("flex flex-wrap items-center gap-1.5", className)}>
      {labels.map((label) => (
        <span
          key={label}
          className="inline-flex items-center rounded-full bg-secondary px-2 py-0.5 text-xs text-secondary-foreground"
        >
          {label}
          <button
            type="button"
            onClick={() => removeLabel(label)}
            aria-label={`Remove label ${label}`}
            className="ml-1 cursor-pointer text-muted-foreground transition-colors hover:text-foreground"
          >
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}

      {adding ? (
        <input
          ref={inputRef}
          type="text"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleKeyDown}
          onBlur={handleBlur}
          placeholder="Label name..."
          className={cn(
            "h-5 w-24 rounded-full border border-dashed border-input bg-background",
            "px-2 py-0 text-xs text-foreground placeholder:text-muted-foreground",
            "focus:outline-none focus:ring-1 focus:ring-ring",
          )}
        />
      ) : (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className={cn(
            "inline-flex h-5 items-center gap-0.5 rounded-full border border-dashed",
            "border-border px-2 text-xs text-muted-foreground",
            "transition-colors hover:border-foreground/40 hover:bg-accent hover:text-accent-foreground",
          )}
        >
          <Plus className="h-3 w-3" />
          Add label
        </button>
      )}
    </div>
  );
}
