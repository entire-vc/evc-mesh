// Names of views, dimensions, windows and filters, one place for the page, the widget and the settings form.
import type { ST } from "./format";
import type { StatsView } from "./state";
import type { FilterKey, QuotaWindow, UsageDimension } from "./types";

export function viewLabel(v: StatsView, st: ST): string {
  switch (v) {
    case "cost":
      return st("view.cost", "≈ $ at API price");
    case "tokens":
      return st("view.tokens", "Tokens");
    case "quota":
      return st("view.quota", "Subscription limits");
    case "turns":
      return st("view.turns", "Spawns");
    case "duration":
      return st("view.duration", "Run time");
    default:
      return st("view.cards_done", "Closed cards");
  }
}

export function dimensionLabel(d: UsageDimension, st: ST): string {
  switch (d) {
    case "model":
      return st("dim.model", "Model");
    case "provider":
      return st("dim.provider", "Provider");
    case "account":
      return st("dim.account", "Pool / account");
    case "agent":
      return st("dim.agent", "Agent");
    case "project":
      return st("dim.project", "Project");
    default:
      return st("dim.workspace", "Workspace");
  }
}

/** Heading of the breakdown table: «By model». */
export function byDimension(d: UsageDimension, st: ST): string {
  switch (d) {
    case "model":
      return st("by.model", "By model");
    case "provider":
      return st("by.provider", "By provider");
    case "account":
      return st("by.account", "By pool");
    case "agent":
      return st("by.agent", "By agent");
    case "project":
      return st("by.project", "By project");
    default:
      return st("by.workspace", "By workspace");
  }
}

export function windowLabel(w: QuotaWindow, st: ST): string {
  switch (w) {
    case "5h":
      return st("window.5h", "5 hours");
    case "week":
      return st("window.week", "Week");
    case "month":
      return st("window.month", "Month");
    default:
      return st("window.budget", "Budget");
  }
}

export function filterLabel(k: FilterKey, st: ST): string {
  if (k === "billing_mode") return st("filter.billing_mode", "Billing");
  return dimensionLabel(k, st);
}
