import type { MouseEvent, ReactNode } from "react";
import { useCoreActions, useTaskHrefFor } from "./actions";
import type { Id } from "./types";

/**
 * A real href (new tab, copy link) with an in-app click: a plain left click
 * calls actions.openTask; modified and middle clicks are left to the browser.
 */
export function TaskLink({
  taskId,
  children,
  className,
}: {
  taskId: Id;
  children: ReactNode;
  className?: string;
}) {
  const href = useTaskHrefFor(taskId);
  const { openTask } = useCoreActions();
  const onClick = (e: MouseEvent<HTMLAnchorElement>) => {
    // A click on the link is never a click on an editor wrapped around it (InlineEdit), modified or not.
    e.stopPropagation();
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    openTask(taskId);
  };
  return (
    <a href={href} className={className} onClick={onClick}>
      {children}
    </a>
  );
}
