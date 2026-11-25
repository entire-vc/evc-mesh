import type { ComponentType } from "react";
import { cn } from "@/lib/cn";
import { EntityIcon } from "@/components/ui/entity-icon";
import { parseIconValue } from "@/core/icons/value";

export interface ProjectLabelProps {
  /** The project's stored icon: "", an emoji, `icon:<name>` or a legacy text icon. */
  icon: string | null | undefined;
  name: string;
  /** Drawn when the project has no icon; without it an iconless project is the bare name. */
  fallback?: ComponentType<{ className?: string }>;
  className?: string;
}

/**
 * The one "icon + project name" label for lists, widgets and search results.
 * The icon is sized by the surrounding text: an emoji is drawn at the text's own size and is never
 * squeezed into a fixed box (a 10px box made a wider emoji glyph run into the name), a drawn icon
 * is 1em square. The gap sits on the spacing scale and exists only when there is an icon; both
 * parts are centred on the line. The name truncates, the icon never shrinks.
 */
export function ProjectLabel({ icon, name, fallback, className }: ProjectLabelProps) {
  const kind = parseIconValue(icon).kind;
  const hasIcon = kind !== "none" || !!fallback;
  return (
    <span data-project-label="" className={cn("inline-flex min-w-0 max-w-full items-center", hasIcon && "gap-1", className)}>
      {hasIcon ? (
        <EntityIcon
          value={icon}
          fallback={fallback}
          className={cn("shrink-0", kind === "emoji" || kind === "text" ? "leading-none" : "h-[1em] w-[1em]")}
        />
      ) : null}
      <span className="min-w-0 truncate">{name}</span>
    </span>
  );
}
