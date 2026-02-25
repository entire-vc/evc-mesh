import { AlertTriangle, Flag } from "lucide-react";
import { cn } from "@/lib/cn";
import type { Priority } from "@/types";

const priorityMeta: Record<
  Priority,
  { label: string; colorClass: string; iconSize: Record<"sm" | "md", number> }
> = {
  urgent: {
    label: "Urgent",
    colorClass: "text-red-500",
    iconSize: { sm: 14, md: 16 },
  },
  high: {
    label: "High",
    colorClass: "text-orange-500",
    iconSize: { sm: 14, md: 16 },
  },
  medium: {
    label: "Medium",
    colorClass: "text-yellow-500",
    iconSize: { sm: 14, md: 16 },
  },
  low: {
    label: "Low",
    colorClass: "text-blue-400",
    iconSize: { sm: 14, md: 16 },
  },
  none: {
    label: "No priority",
    colorClass: "text-gray-300 dark:text-gray-600",
    iconSize: { sm: 14, md: 16 },
  },
};

export interface PriorityFlagProps {
  priority: Priority;
  showLabel?: boolean;
  size?: "sm" | "md";
  className?: string;
}

export function PriorityFlag({
  priority,
  showLabel = false,
  size = "md",
  className,
}: PriorityFlagProps) {
  const meta = priorityMeta[priority];
  const px = meta.iconSize[size];

  const Icon = priority === "urgent" ? AlertTriangle : Flag;

  return (
    <span
      className={cn("inline-flex items-center gap-1", meta.colorClass, className)}
      title={meta.label}
      aria-label={meta.label}
    >
      <Icon width={px} height={px} aria-hidden="true" />
      {showLabel && (
        <span className={cn("font-medium", size === "sm" ? "text-xs" : "text-sm")}>
          {meta.label}
        </span>
      )}
    </span>
  );
}
