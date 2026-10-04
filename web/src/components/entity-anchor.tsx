import { useEffect, useRef, type ReactNode } from "react";
import { cn } from "@/lib/cn";

/** Same persistent highlight and scroll treatment as a linked comment. */
export function EntityAnchor({ id, kind, focused, className, children }: {
  id: string;
  kind: "artifact" | "schedule";
  focused: boolean;
  className?: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (focused) ref.current?.scrollIntoView({ block: "nearest" });
  }, [focused]);
  return <div ref={ref} data-artifact-id={kind === "artifact" ? id : undefined}
    data-schedule-id={kind === "schedule" ? id : undefined} data-focused={focused ? "true" : undefined}
    className={cn(className, focused && "rounded-lg ring-2 ring-yellow-400 bg-yellow-50/60 dark:bg-yellow-400/10")}>
    {children}
  </div>;
}
