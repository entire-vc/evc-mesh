import { useState } from "react";
import { BookOpen, FileText, Paperclip, Upload } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { AttachDocDialog } from "@/components/AttachDocDialog";
import type { DocumentSearchHit } from "@/lib/docs/document-search";
import { cn } from "@/lib/cn";

/**
 * One control for the three ways an attachment can arrive: the computer, a
 * Doc already in this project, or a document mounted from Obsidian via Team
 * Relay. Before this, a surface that wanted all three had to build three
 * separate habits — see the R6 task for where that showed up. This is the
 * one control; a surface using it commits to exactly one place a writer
 * looks for "attach something".
 *
 * The Obsidian item can be present-but-disabled with a reason (rather than
 * simply absent) — this is for a surface where inserting a relay:// link
 * would be wrong right now (the surface IS itself a mounted Team Relay copy,
 * and there is no write-back yet). A disabled control with a reason is more
 * honest than a control that silently inserts something that breaks on the
 * other side.
 */

interface AttachmentSourceMenuProps {
  projId: string | undefined;
  hasTrIntegration: boolean;
  /** Set when this surface is itself a Team-Relay-mounted copy — see file docstring. */
  obsidianDisabledReason?: string;
  disabled?: boolean;
  disabledTitle?: string;
  onPickFiles: () => void;
  onPickDoc: (hit: DocumentSearchHit) => void;
  onPickRelay: (hit: DocumentSearchHit) => void;
  className?: string;
  /** Trigger label. Defaults to an icon-only paperclip button. */
  label?: string;
}

export function AttachmentSourceMenu({
  projId,
  hasTrIntegration,
  obsidianDisabledReason,
  disabled = false,
  disabledTitle,
  onPickFiles,
  onPickDoc,
  onPickRelay,
  className,
  label,
}: AttachmentSourceMenuProps) {
  const [docDialogOpen, setDocDialogOpen] = useState(false);
  const [relayDialogOpen, setRelayDialogOpen] = useState(false);

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          disabled={disabled}
          title={disabled ? disabledTitle : "Attach"}
          className={cn(
            "flex items-center gap-1.5 rounded-md px-2 py-1 text-sm font-medium text-primary hover:underline disabled:cursor-not-allowed disabled:text-muted-foreground disabled:no-underline disabled:opacity-60",
            className,
          )}
        >
          <Paperclip className="h-3.5 w-3.5" />
          {label ?? "Attach"}
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem onClick={onPickFiles}>
            <Upload className="mr-2 h-3.5 w-3.5" />
            From computer
          </DropdownMenuItem>
          {projId && (
            <DropdownMenuItem onClick={() => setDocDialogOpen(true)}>
              <FileText className="mr-2 h-3.5 w-3.5" />
              From Docs
            </DropdownMenuItem>
          )}
          {hasTrIntegration && projId && (
            <DropdownMenuItem
              aria-disabled={Boolean(obsidianDisabledReason)}
              title={obsidianDisabledReason}
              onClick={() => {
                if (obsidianDisabledReason) return;
                setRelayDialogOpen(true);
              }}
              className={
                obsidianDisabledReason
                  ? "cursor-not-allowed items-start text-muted-foreground opacity-60 hover:bg-transparent hover:text-muted-foreground"
                  : undefined
              }
            >
              <BookOpen className="mr-2 mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>
                From Obsidian
                {obsidianDisabledReason && (
                  <span className="block text-[11px] leading-tight">
                    {obsidianDisabledReason}
                  </span>
                )}
              </span>
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {projId && (
        <AttachDocDialog
          projId={projId}
          scope="docs"
          open={docDialogOpen}
          onClose={() => setDocDialogOpen(false)}
          onSelect={onPickDoc}
        />
      )}
      {hasTrIntegration && projId && !obsidianDisabledReason && (
        <AttachDocDialog
          projId={projId}
          scope="relay"
          open={relayDialogOpen}
          onClose={() => setRelayDialogOpen(false)}
          onSelect={onPickRelay}
        />
      )}
    </>
  );
}
