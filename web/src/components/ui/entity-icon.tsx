import { Component, type ComponentType, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { useCoreIcons } from "@/core/icons/core-loader";
import { getThemeIcons, useThemeIconsVersion } from "@/core/icons/theme-icons";
import { parseIconValue } from "@/core/icons/value";
import { useThemeStore } from "@/core/themes/store";

export interface EntityIconProps {
  /** Stored value: "", an emoji, `icon:<name>`, or a legacy text icon. */
  value: string | null | undefined;
  /** Drawn for an empty value, an unknown name, or while the core set loads. */
  fallback?: ComponentType<{ className?: string }>;
  className?: string;
}

// A theme's icon that throws while rendering must not take the page down.
class IconBoundary extends Component<
  { fallback: ReactNode; children: ReactNode; resetKey: string },
  { failed: boolean; key: string }
> {
  state = { failed: false, key: this.props.resetKey };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  // A different theme, bundle version or name is a new attempt: forget an earlier failure.
  static getDerivedStateFromProps(
    props: { resetKey: string },
    state: { failed: boolean; key: string },
  ) {
    return props.resetKey === state.key ? null : { failed: false, key: props.resetKey };
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

/**
 * The one renderer of an entity's icon. `icon:<name>` resolves to the live workspace theme's icon,
 * then the core icon of that name, then `fallback` (the entity's default). Icons take `currentColor`,
 * so they follow the theme. Emoji are drawn as text; a legacy text icon is shown as it is.
 */
export function EntityIcon({ value, fallback: Fallback, className }: EntityIconProps) {
  const parsed = parseIconValue(value);
  const themeId = useThemeStore((s) => s.viewsThemeId);
  const iconsVersion = useThemeIconsVersion();
  const themeIcon = parsed.kind === "icon" ? getThemeIcons(themeId)?.icons[parsed.name] : undefined;
  const core = useCoreIcons(parsed.kind === "icon" && !themeIcon);

  const fb = Fallback ? <Fallback className={className} /> : null;

  switch (parsed.kind) {
    case "none":
      return fb;
    case "emoji":
      return (
        <span role="img" aria-label={parsed.emoji} className={cn("inline-block leading-none", className)}>
          {parsed.emoji}
        </span>
      );
    case "text":
      return <span className={cn("inline-block leading-none", className)}>{parsed.text}</span>;
    case "icon": {
      const Core = core?.[parsed.name];
      const Icon = themeIcon ?? Core;
      if (!Icon) return fb;
      return (
        <IconBoundary resetKey={`${themeId}:${iconsVersion}:${parsed.name}`} fallback={Core && themeIcon ? <Core className={className} /> : fb}>
          <Icon className={className} />
        </IconBoundary>
      );
    }
  }
}
