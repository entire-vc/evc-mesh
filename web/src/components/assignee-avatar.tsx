import { useTranslation } from "react-i18next";
import { useAgentShortTag } from "@/hooks/use-agent-short-tag";
import { agentLabel } from "@/lib/agent-label";
import { cn } from "@/lib/cn";

// 10 deterministic background colors for named assignees
const AVATAR_COLORS = [
  "bg-avatar-1 text-white",
  "bg-avatar-2 text-white",
  "bg-avatar-3 text-white",
  "bg-avatar-4 text-white",
  "bg-avatar-5 text-white",
  "bg-avatar-6 text-white",
  "bg-avatar-7 text-white",
  "bg-avatar-8 text-white",
  "bg-avatar-9 text-white",
  "bg-avatar-10 text-white",
] as const;

/**
 * Returns a stable color class index based on the characters in the name.
 * Same name always maps to the same color.
 */
function colorIndexForName(name: string): number {
  let hash = 0;
  for (let i = 0; i < name.length; i++) {
    hash = (hash * 31 + name.charCodeAt(i)) >>> 0; // unsigned 32-bit
  }
  return hash % AVATAR_COLORS.length;
}

const sizeClasses = {
  sm: "w-6 h-6 text-xs",
  md: "w-8 h-8 text-sm",
  lg: "w-10 h-10 text-base",
} as const;

export interface AssigneeAvatarProps {
  id?: string | null;
  name?: string | null;
  shortTag?: string | null;
  type?: "user" | "agent" | "unassigned";
  size?: "sm" | "md" | "lg";
  className?: string;
}

/** Looks the short tag up in the agent directory unless `shortTag` is passed. */
export function AssigneeAvatar({ id, shortTag, type = "unassigned", ...rest }: AssigneeAvatarProps) {
  const savedTag = useAgentShortTag(shortTag === undefined ? id : undefined, type);
  return <AssigneeAvatarView {...rest} type={type} shortTag={shortTag === undefined ? savedTag : shortTag} />;
}

/** Pure variant: draws from props only, never touches a store (slot views use it). */
export function AssigneeAvatarView({
  name,
  shortTag,
  type = "unassigned",
  size = "md",
  className,
}: Omit<AssigneeAvatarProps, "id">) {
  const { t } = useTranslation("settings");
  const tag = shortTag;
  const baseClasses = cn(
    "inline-flex shrink-0 items-center justify-center rounded-full font-medium select-none",
    sizeClasses[size],
  );

  // Named assignee — show first two characters with a deterministic color
  if (name) {
    const label = agentLabel(name, type === "agent" ? tag : null);
    const initials = name.slice(0, 2).toUpperCase();
    const colorClass = AVATAR_COLORS[colorIndexForName(name)];
    return (
      <span
        className={cn(baseClasses, colorClass, className)}
        title={label}
        aria-label={label}
      >
        {initials}
      </span>
    );
  }

  // Agent without a name
  if (type === "agent") {
    return (
      <span
        className={cn(baseClasses, "bg-tone-violet-100 text-tone-violet-600 dark:bg-tone-violet-900 dark:text-tone-violet-300", className)}
        title={t("avatar.aiAgent")}
        aria-label={t("avatar.aiAgent")}
      >
        AI
      </span>
    );
  }

  // Human user without a name
  if (type === "user") {
    return (
      <span
        className={cn(baseClasses, "bg-tone-sky-100 text-tone-sky-600 dark:bg-tone-sky-900 dark:text-tone-sky-300", className)}
        title={t("avatar.user")}
        aria-label={t("avatar.user")}
      >
        U
      </span>
    );
  }

  // Unassigned — dashed outline circle, no fill
  return (
    <span
      className={cn(
        baseClasses,
        "border-2 border-dashed border-muted-foreground/40 text-muted-foreground/40",
        className,
      )}
      title={t("avatar.unassigned")}
      aria-label={t("avatar.unassigned")}
    >
      {/* intentionally empty */}
    </span>
  );
}
