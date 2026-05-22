import { useEffect, useRef } from "react";
import { useWorkspaceStore } from "@/stores/workspace";

const DEFAULT_FAVICON = "/favicon.ico";

export function useDynamicFavicon() {
  const iconURL = useWorkspaceStore((s) => s.currentWorkspace?.icon_url ?? null);
  const prevIconURL = useRef<string | null>(null);

  useEffect(() => {
    if (iconURL === prevIconURL.current) return;
    prevIconURL.current = iconURL;

    let link = document.querySelector<HTMLLinkElement>("link[rel~='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }

    link.href = iconURL ?? DEFAULT_FAVICON;
  }, [iconURL]);
}
