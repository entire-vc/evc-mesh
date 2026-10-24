import { useMemo } from "react";
import "../mesh-default";
import { Slot } from "../registry";
import { useBuildSlotContext } from "../use-slot-context";
import type { AuthField, AuthScreenActions, AuthScreenData } from "./types";

// Instance-level theme settings only: the workspace is unknown before sign-in
// and no theme is installed yet (T5), so there is nothing to merge.
const NO_SETTINGS = {};

export function AuthScreenSlot({
  data,
  onSetValue,
  onSubmit,
  onNavigate,
}: {
  data: AuthScreenData;
  onSetValue: (name: AuthField["name"], value: string) => void;
  onSubmit: () => Promise<void>;
  onNavigate: (to: string) => void;
}) {
  const ctx = useBuildSlotContext("auth.screen", null);
  const actions: AuthScreenActions = useMemo(
    () => ({ setValue: onSetValue, submit: onSubmit, navigate: onNavigate }),
    [onSetValue, onSubmit, onNavigate],
  );
  return <Slot name="auth.screen" data={data} actions={actions} themeSettings={NO_SETTINGS} ctx={ctx} />;
}

/** `setValue` over a record of React state setters keyed by field name. */
export function useSetValue(setters: Partial<Record<AuthField["name"], (v: string) => void>>) {
  return (name: AuthField["name"], value: string) => setters[name]?.(value);
}
