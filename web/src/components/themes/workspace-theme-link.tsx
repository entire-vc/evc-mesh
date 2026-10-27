import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { Palette } from "lucide-react";
import { getResolvedWorkspaceTheme, listThemes } from "@/lib/themes";

/**
 * The workspace theme is set in Extensions, like any extension (П8): Workspace Settings
 * only points there, to the theme the workspace paints with now.
 */
export function WorkspaceThemeLink({ wsSlug }: { wsSlug: string }) {
  const { t } = useTranslation("settings");
  // undefined = loading; null = not known (the link then goes to the themes in the catalog)
  const [active, setActive] = useState<{ id: string; name: string } | null | undefined>();

  useEffect(() => {
    let cancelled = false;
    setActive(undefined);
    Promise.all([getResolvedWorkspaceTheme(wsSlug), listThemes().catch(() => null)])
      .then(([r, list]) => {
        if (cancelled) return;
        const id = r?.theme_id;
        if (!id) return setActive(null);
        setActive({ id, name: list?.themes.find((x) => x.id === id)?.name ?? id });
      })
      .catch(() => !cancelled && setActive(null));
    return () => { cancelled = true; };
  }, [wsSlug]);

  const to = active ? `/w/${wsSlug}/extensions/${encodeURIComponent(active.id)}` : `/w/${wsSlug}/extensions?kind=theme`;
  return (
    <div className="flex items-start gap-3" data-testid="workspace-theme-link">
      <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <Palette className="h-5 w-5" aria-hidden />
      </span>
      <div className="min-w-0 space-y-1">
        {active === undefined ? (
          <div role="status" aria-busy="true" className="h-5 w-48 animate-pulse rounded bg-muted/50" />
        ) : (
          <Link to={to} className="text-sm font-medium text-primary hover:underline">
            {active ? t("themes.workspace.extensionsLink", { name: active.name }) : t("themes.workspace.extensionsLinkAll")}
          </Link>
        )}
        <p className="text-xs text-muted-foreground">{t("themes.workspace.movedHint")}</p>
      </div>
    </div>
  );
}
