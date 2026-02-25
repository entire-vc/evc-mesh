import { cn } from "@/lib/cn";

// 10 deterministic background colors for named assignees
const AVATAR_COLORS = [
  "bg-teal-500 text-white",
  "bg-violet-500 text-white",
  "bg-sky-500 text-white",
  "bg-orange-500 text-white",
  "bg-emerald-500 text-white",
  "bg-pink-500 text-white",
  "bg-amber-500 text-white",
  "bg-indigo-500 text-white",
  "bg-rose-500 text-white",
  "bg-cyan-500 text-white",
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
  name?: string | null;
  type?: "user" | "agent" | "unassigned";
  size?: "sm" | "md" | "lg";
  className?: string;
}

export function AssigneeAvatar({
  name,
  type = "unassigned",
  size = "md",
  className,
}: AssigneeAvatarProps) {
  const baseClasses = cn(
    "inline-flex shrink-0 items-center justify-center rounded-full font-medium select-none",
    sizeClasses[size],
  );

  // Named assignee — show first two characters with a deterministic color
  if (name) {
    const initials = name.slice(0, 2).toUpperCase();
    const colorClass = AVATAR_COLORS[colorIndexForName(name)];
    return (
      <span
        className={cn(baseClasses, colorClass, className)}
        title={name}
        aria-label={name}
      >
        {initials}
      </span>
    );
  }

  // Agent without a name
  if (type === "agent") {
    return (
      <span
        className={cn(baseClasses, "bg-violet-100 text-violet-600 dark:bg-violet-900 dark:text-violet-300", className)}
        title="AI agent"
        aria-label="AI agent"
      >
        AI
      </span>
    );
  }

  // Human user without a name
  if (type === "user") {
    return (
      <span
        className={cn(baseClasses, "bg-sky-100 text-sky-600 dark:bg-sky-900 dark:text-sky-300", className)}
        title="User"
        aria-label="User"
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
      title="Unassigned"
      aria-label="Unassigned"
    >
      {/* intentionally empty */}
    </span>
  );
}
