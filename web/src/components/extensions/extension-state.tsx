import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { EXTENSION_STATE_KEY, KEYS_FROM_KEY, type ExtensionState, type ExtensionStateName } from "@/lib/extensionState";
import { cn } from "@/lib/cn";

const VARIANT: Record<ExtensionStateName, "warning" | "outline" | "success" | "destructive"> = {
  not_allowed: "warning",
  available: "outline",
  enabled: "success",
  error: "destructive",
  blocked: "destructive",
};

/**
 * The state label every extension list shows, from `extensionState`. `scope="instance"`
 * is the instance admin's view: there "enabled" means on for the whole instance.
 */
export function ExtensionStateBadge({ state, scope = "workspace", className }: {
  state: ExtensionState; scope?: "workspace" | "instance"; className?: string;
}) {
  const { t } = useTranslation("integrations");
  const key = scope === "instance" && state.state === "enabled" ? "extensionState.enabledInstance" : EXTENSION_STATE_KEY[state.state];
  return <Badge variant={VARIANT[state.state]} className={className} data-state={state.state}>{t(key)}</Badge>;
}

/** The line under the label: whose keys, the last error, or the catalog's reason. Null when there is nothing to add. */
export function ExtensionStateNote({ state, className }: { state: ExtensionState; className?: string }) {
  const { t } = useTranslation("integrations");
  let text = "";
  let tone = "text-muted-foreground";
  if (state.state === "enabled" && state.keysFrom) text = t(KEYS_FROM_KEY[state.keysFrom]);
  else if (state.state === "error") { text = state.error || t("extensionState.errorUnknown"); tone = "text-destructive"; }
  else if (state.state === "blocked") { text = state.blockedReason || t("extensionState.blockedNoReason"); tone = "text-destructive"; }
  if (!text) return null;
  return <p className={cn("break-words text-xs", tone, className)}>{text}</p>;
}
