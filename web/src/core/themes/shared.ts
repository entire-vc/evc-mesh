// The single copies of React & co that theme bundles share (ADR D10). The
// import map in index.html maps `react`, `react-dom`, `react/jsx-runtime`,
// `react-i18next` and `@entire-vc/mesh-theme-sdk` to /theme-shared/*.js; those
// shims re-export from `window.__MESH_SHARED__`, assigned here before any
// bundle can load. `scripts/gen-theme-shims.mjs` writes the shims.
import * as React from "react";
import * as ReactDOM from "react-dom";
import * as JsxRuntime from "react/jsx-runtime";
import * as I18Next from "react-i18next";
import type * as SdkPackage from "@entire-vc/mesh-theme-sdk";
import { BOARD_CONTRACT_VERSION, CONTRACT_VERSION, DOC_CONTRACT_VERSION } from "@entire-vc/mesh-theme-sdk";
import { Can, DndProvider, Draggable, DropZone, InlineEdit, TaskBadges, TaskLink, useCan, useRealtime } from "@/core/primitives";
import { useActiveSessions, useMentions, useMyTasks, useTasks, useTriageTasks, useWidgetData } from "@/core/dashboard/hooks";
import { useSlotContext } from "@/core/slots/slot-context";

// Typed against the SDK's own export list: a name the SDK declares and the core does not provide, or
// provides with another signature, fails the typecheck. gen-theme-shims writes the shim from the same list.
const sdk: typeof SdkPackage = {
  BOARD_CONTRACT_VERSION,
  CONTRACT_VERSION,
  DOC_CONTRACT_VERSION,
  Can,
  DndProvider,
  Draggable,
  DropZone,
  InlineEdit,
  TaskBadges,
  TaskLink,
  useCan,
  useRealtime,
  useTasks,
  useWidgetData,
  useMentions,
  useMyTasks,
  useTriageTasks,
  useActiveSessions,
  useSlotContext,
};

export const SHARED_MODULES = {
  react: React,
  "react-dom": ReactDOM,
  "react/jsx-runtime": JsxRuntime,
  "react-i18next": I18Next,
  "@entire-vc/mesh-theme-sdk": sdk,
} as const;

export function exposeSharedModules(): void {
  (window as unknown as { __MESH_SHARED__: unknown }).__MESH_SHARED__ = SHARED_MODULES;
}
