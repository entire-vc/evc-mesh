// src/card.tsx
import { Can, Draggable, TaskBadges, TaskLink } from "@entire-vc/mesh-theme-sdk";

// src/copy.json
var copy_default = {
  en: {
    everyone: "Everyone",
    unassigned: "Unassigned",
    focusOn: "Highlight work of",
    retry: "Retry",
    refreshing: "Refreshing\u2026",
    empty: "Nothing here",
    emptyBand: "Drop a card here",
    noStatuses: "This project has no statuses yet",
    noMatches: "No cards match the filters",
    closedPartial: "Showing {loaded} of {total} closed cards",
    loadMore: "Load more",
    loading: "Loading\u2026",
    add: "Add card",
    raise: "Raise priority",
    postpone: "Postpone one day",
    open: "Open",
    overLimit: "Over the limit of {limit}",
    due: "Due {date}",
    overdue: "Overdue since {date}",
    gate: "Waiting for a person",
    subtasks: "{count} subtasks",
    dashTitle: "Team focus",
    noWidgets: "No widgets to show",
    focusTitle: "Due this week",
    focusEmpty: "Nothing due in the next {days} days",
    focusError: "Could not load your tasks",
    overdueGroup: "Overdue",
    soonGroup: "Coming up"
  },
  ru: {
    everyone: "\u0412\u0441\u0435",
    unassigned: "\u0411\u0435\u0437 \u0438\u0441\u043F\u043E\u043B\u043D\u0438\u0442\u0435\u043B\u044F",
    focusOn: "\u041F\u043E\u0434\u0441\u0432\u0435\u0442\u0438\u0442\u044C \u0440\u0430\u0431\u043E\u0442\u0443",
    retry: "\u041F\u043E\u0432\u0442\u043E\u0440\u0438\u0442\u044C",
    refreshing: "\u041E\u0431\u043D\u043E\u0432\u043B\u044F\u0435\u043C\u2026",
    empty: "\u0417\u0434\u0435\u0441\u044C \u043F\u0443\u0441\u0442\u043E",
    emptyBand: "\u041F\u0435\u0440\u0435\u0442\u0430\u0449\u0438\u0442\u0435 \u043A\u0430\u0440\u0442\u043E\u0447\u043A\u0443 \u0441\u044E\u0434\u0430",
    noStatuses: "\u0412 \u043F\u0440\u043E\u0435\u043A\u0442\u0435 \u0435\u0449\u0451 \u043D\u0435\u0442 \u0441\u0442\u0430\u0442\u0443\u0441\u043E\u0432",
    noMatches: "\u041D\u0435\u0442 \u043A\u0430\u0440\u0442\u043E\u0447\u0435\u043A \u043F\u043E\u0434 \u0444\u0438\u043B\u044C\u0442\u0440\u044B",
    closedPartial: "\u041F\u043E\u043A\u0430\u0437\u0430\u043D\u043E {loaded} \u0438\u0437 {total} \u0437\u0430\u043A\u0440\u044B\u0442\u044B\u0445 \u043A\u0430\u0440\u0442\u043E\u0447\u0435\u043A",
    loadMore: "\u041F\u043E\u043A\u0430\u0437\u0430\u0442\u044C \u0435\u0449\u0451",
    loading: "\u0417\u0430\u0433\u0440\u0443\u0436\u0430\u0435\u043C\u2026",
    add: "\u0414\u043E\u0431\u0430\u0432\u0438\u0442\u044C \u043A\u0430\u0440\u0442\u043E\u0447\u043A\u0443",
    raise: "\u041F\u043E\u0434\u043D\u044F\u0442\u044C \u043F\u0440\u0438\u043E\u0440\u0438\u0442\u0435\u0442",
    postpone: "\u0421\u0434\u0432\u0438\u043D\u0443\u0442\u044C \u043D\u0430 \u0434\u0435\u043D\u044C",
    open: "\u041E\u0442\u043A\u0440\u044B\u0442\u044C",
    overLimit: "\u0412\u044B\u0448\u0435 \u043B\u0438\u043C\u0438\u0442\u0430 {limit}",
    due: "\u0421\u0440\u043E\u043A {date}",
    overdue: "\u041F\u0440\u043E\u0441\u0440\u043E\u0447\u0435\u043D\u043E \u0441 {date}",
    gate: "\u0416\u0434\u0451\u0442 \u0447\u0435\u043B\u043E\u0432\u0435\u043A\u0430",
    subtasks: "\u041F\u043E\u0434\u0437\u0430\u0434\u0430\u0447: {count}",
    dashTitle: "\u0424\u043E\u043A\u0443\u0441 \u043A\u043E\u043C\u0430\u043D\u0434\u044B",
    noWidgets: "\u041D\u0435\u0442 \u0432\u0438\u0434\u0436\u0435\u0442\u043E\u0432 \u0434\u043B\u044F \u043F\u043E\u043A\u0430\u0437\u0430",
    focusTitle: "\u0421\u0440\u043E\u043A\u0438 \u043D\u0430 \u044D\u0442\u043E\u0439 \u043D\u0435\u0434\u0435\u043B\u0435",
    focusEmpty: "\u0412 \u0431\u043B\u0438\u0436\u0430\u0439\u0448\u0438\u0435 {days} \u0434\u043D. \u0441\u0440\u043E\u043A\u043E\u0432 \u043D\u0435\u0442",
    focusError: "\u041D\u0435 \u0443\u0434\u0430\u043B\u043E\u0441\u044C \u0437\u0430\u0433\u0440\u0443\u0437\u0438\u0442\u044C \u0432\u0430\u0448\u0438 \u0437\u0430\u0434\u0430\u0447\u0438",
    overdueGroup: "\u041F\u0440\u043E\u0441\u0440\u043E\u0447\u0435\u043D\u043E",
    soonGroup: "\u0421\u043A\u043E\u0440\u043E"
  }
};

// src/logic.ts
var dicts = copy_default;
function say(locale, key, vars = {}) {
  const lang = (locale ?? "en").slice(0, 2).toLowerCase();
  const raw = dicts[lang]?.[key] ?? dicts.en[key] ?? key;
  return raw.replace(/\{(\w+)\}/g, (_, k) => String(vars[k] ?? ""));
}
var PRIORITIES = ["none", "low", "medium", "high", "urgent"];
function raised(p) {
  const i = PRIORITIES.indexOf(p);
  return PRIORITIES[Math.min(PRIORITIES.length - 1, Math.max(0, i) + 1)];
}
function nextDay(date, now = /* @__PURE__ */ new Date()) {
  const base = date ? new Date(date) : now;
  const d = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth(), base.getUTCDate() + 1));
  return d.toISOString().slice(0, 10);
}
var NOBODY = "none";
function lanes(cards) {
  const by = /* @__PURE__ */ new Map();
  for (const c of cards) {
    const key = c.assignee?.id ?? NOBODY;
    const lane = by.get(key) ?? { key, name: c.assignee?.name ?? "", count: 0 };
    lane.count += 1;
    by.set(key, lane);
  }
  return [...by.values()].sort((a, b) => a.key === NOBODY ? 1 : b.key === NOBODY ? -1 : b.count - a.count || a.name.localeCompare(b.name));
}
function dimmed(focus2, assigneeId) {
  if (!focus2) return false;
  return (assigneeId ?? NOBODY) !== focus2;
}
function initials(name) {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  return (parts.length === 1 ? parts[0].slice(0, 2) : parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}
function wip(count, limit) {
  if (!limit || limit <= 0) return "none";
  return count > limit ? "over" : count === limit ? "full" : "ok";
}
var DAY = 864e5;
function focus(tasks, horizonDays, now = Date.now()) {
  const dated = tasks.filter((t) => !!t.due_date).map((t) => ({ t, at: new Date(t.due_date).getTime() })).filter((x) => !Number.isNaN(x.at));
  const byDate = (a, b) => a.at - b.at;
  const startOfToday = now - now % DAY;
  return {
    overdue: dated.filter((x) => x.at < startOfToday).sort(byDate).map((x) => x.t),
    soon: dated.filter((x) => x.at >= startOfToday && x.at < startOfToday + (horizonDays + 1) * DAY).sort(byDate).map((x) => x.t)
  };
}
function horizon(v) {
  const n = typeof v === "number" ? Math.round(v) : NaN;
  return Number.isFinite(n) && n >= 1 && n <= 30 ? n : 7;
}

// src/card.tsx
import { Fragment, jsx, jsxs } from "react/jsx-runtime";
var text = (v) => v == null || v === "" ? "" : Array.isArray(v) ? v.join(", ") : typeof v === "boolean" ? v ? "\u2713" : "" : String(v);
var day = (iso) => iso.slice(0, 10);
function Meta({ f, task, data, locale }) {
  switch (f.key) {
    case "assignee":
      return /* @__PURE__ */ jsxs("span", { className: "studio-who", "data-field": "assignee", children: [
        /* @__PURE__ */ jsx("span", { className: "studio-avatar", "aria-hidden": "true", children: initials(task.assignee?.name ?? "") }),
        /* @__PURE__ */ jsx("span", { className: "studio-who-name", children: task.assignee?.name ?? say(locale, "unassigned") })
      ] });
    case "due_date":
      return task.dueDate ? /* @__PURE__ */ jsx("span", { className: "studio-due", "data-overdue": task.overdue || void 0, "data-field": "due_date", children: say(locale, task.overdue ? "overdue" : "due", { date: day(task.dueDate) }) }) : null;
    case "gate":
      return task.gate ? /* @__PURE__ */ jsx("span", { className: "studio-flag", "data-field": "gate", title: task.gate.reason || void 0, children: say(locale, "gate") }) : null;
    case "labels":
      return task.labels.length ? /* @__PURE__ */ jsx("ul", { className: "studio-labels", "data-field": "labels", children: task.labels.map((l) => /* @__PURE__ */ jsx("li", { children: l }, l)) }) : null;
    case "subtasks":
      return task.counts.subtasks ? /* @__PURE__ */ jsx("span", { className: "studio-count", "data-field": "subtasks", children: say(locale, "subtasks", { count: task.counts.subtasks }) }) : null;
    case "estimate":
      return task.estimatedHours != null ? /* @__PURE__ */ jsxs("span", { className: "studio-count", "data-field": "estimate", children: [
        task.estimatedHours,
        "h"
      ] }) : null;
    default: {
      if (!f.key.startsWith("cf:")) return null;
      const v = text(task.customFields[f.key.slice(3)]);
      return v ? /* @__PURE__ */ jsxs("span", { className: "studio-count", "data-field": f.key, children: [
        f.label,
        ": ",
        v
      ] }) : null;
    }
  }
}
function Body({ data, actions, themeSettings, locale, plain }) {
  const { task } = data;
  const cover = themeSettings.cover_style === "none" || themeSettings.cover_style === "strip" ? themeSettings.cover_style : "block";
  const dim = dimmed(themeSettings.studio_focus, task.assignee?.id);
  return /* @__PURE__ */ jsxs("article", { className: "studio-card", "data-priority": task.priority, "data-cover": cover, "data-dim": dim || void 0, "data-testid": "studio-card", children: [
    cover !== "none" && /* @__PURE__ */ jsx("span", { className: "studio-cover", "aria-hidden": "true" }),
    /* @__PURE__ */ jsxs("div", { className: "studio-body", children: [
      /* @__PURE__ */ jsx("h3", { className: "studio-title", children: plain ? task.title : /* @__PURE__ */ jsx(TaskLink, { taskId: task.id, children: task.title }) }),
      /* @__PURE__ */ jsxs("div", { className: "studio-meta", children: [
        data.fields.map((f) => /* @__PURE__ */ jsx(Meta, { f, task, data, locale }, f.key)),
        /* @__PURE__ */ jsx(TaskBadges, { task })
      ] }),
      !plain && actions && /* @__PURE__ */ jsx(Can, { permission: "task.edit", resource: { kind: "task", id: task.id }, children: /* @__PURE__ */ jsxs("div", { className: "studio-actions", role: "group", "aria-label": task.title, children: [
        actions.quickEdit && /* @__PURE__ */ jsxs(Fragment, { children: [
          /* @__PURE__ */ jsx("button", { type: "button", className: "studio-act", "data-action": "raise", disabled: task.priority === "urgent", onClick: () => void actions.quickEdit?.({ field: "priority", value: raised(task.priority) }), children: say(locale, "raise") }),
          /* @__PURE__ */ jsx("button", { type: "button", className: "studio-act", "data-action": "postpone", onClick: () => void actions.quickEdit?.({ field: "due_date", value: nextDay(task.dueDate) }), children: say(locale, "postpone") })
        ] }),
        /* @__PURE__ */ jsx("button", { type: "button", className: "studio-act", "data-action": "open", onClick: () => actions.openTask(), children: say(locale, "open") })
      ] }) })
    ] })
  ] });
}
var Card = ({ data, actions, themeSettings, ctx }) => {
  if (data.isOverlay) return /* @__PURE__ */ jsx(Body, { data, themeSettings, locale: ctx.locale, plain: true });
  return /* @__PURE__ */ jsx(Draggable, { item: { kind: "task", id: data.task.id }, label: data.task.title, "data-testid": "task-card", "data-task-id": data.task.id, children: /* @__PURE__ */ jsx(Body, { data, actions, themeSettings, locale: ctx.locale }) });
};

// src/column.tsx
import { memo, useMemo, useState } from "react";
import { DropZone } from "@entire-vc/mesh-theme-sdk";
import { Fragment as Fragment2, jsx as jsx2, jsxs as jsxs2 } from "react/jsx-runtime";
var BandCard = memo(function BandCard2({ Card: Card2, task, column, fields, openTask, quickEdit, themeSettings, ctx }) {
  const data = useMemo(
    () => ({ task, column: { id: column.id, kind: column.kind, statusCategory: column.status?.category }, gateStatusName: column.gateStatusNameByTask?.[task.id], fields, selected: false, isOverlay: false }),
    [task, column.id, column.kind, column.status?.category, column.gateStatusNameByTask, fields]
  );
  const actions = useMemo(
    () => ({
      openTask: (opts) => openTask(task.id, opts),
      quickEdit: quickEdit ? (patch) => quickEdit(task.id, patch) : void 0
    }),
    [openTask, quickEdit, task.id]
  );
  return /* @__PURE__ */ jsx2(Card2, { data, actions, themeSettings, ctx });
});
var Column = ({ data, actions, themeSettings, ctx }) => {
  const { column, dnd, fields, components } = data;
  const locale = ctx.locale;
  const ids = useMemo(() => column.tasks.map((t) => t.id), [column.tasks]);
  const [busy, setBusy] = useState(false);
  const [loadError, setLoadError] = useState(null);
  const state = themeSettings.show_wip === false ? "none" : wip(column.totalCount, column.wipLimit);
  return /* @__PURE__ */ jsxs2("section", { className: "studio-band", "data-column": column.id, "data-wip": state, "aria-label": column.title, children: [
    /* @__PURE__ */ jsxs2("header", { className: "studio-rail", children: [
      /* @__PURE__ */ jsx2("span", { className: "studio-dot", style: column.color ? { backgroundColor: column.color } : void 0, "aria-hidden": "true" }),
      /* @__PURE__ */ jsx2("h2", { className: "studio-status", children: column.title }),
      /* @__PURE__ */ jsx2("span", { className: "studio-total", "data-testid": "column-count", children: column.countLabel }),
      state !== "none" && column.wipLimit != null && /* @__PURE__ */ jsxs2("span", { className: "studio-wip", title: state === "over" ? say(locale, "overLimit", { limit: column.wipLimit }) : void 0, children: [
        column.totalCount,
        "/",
        column.wipLimit
      ] }),
      actions.createTask && /* @__PURE__ */ jsx2("button", { type: "button", className: "studio-add", onClick: () => actions.createTask?.(), "aria-label": `${say(locale, "add")}: ${column.title}`, children: "+" })
    ] }),
    /* @__PURE__ */ jsx2(
      DropZone,
      {
        zone: { kind: "status", id: column.id },
        orientation: "horizontal",
        itemIds: ids,
        enabled: dnd.enabled && column.kind === "status",
        "data-testid": "board-column",
        "data-column-id": column.id,
        "data-status-id": column.status?.id ?? "",
        className: ({ isOver }) => `studio-drop${isOver ? " is-over" : ""}`,
        children: /* @__PURE__ */ jsxs2(Fragment2, { children: [
          column.tasks.map((task) => /* @__PURE__ */ jsx2(BandCard, { Card: components.Card, task, column, fields, openTask: actions.openTask, quickEdit: actions.quickEdit, themeSettings, ctx }, task.id)),
          column.tasks.length === 0 && /* @__PURE__ */ jsx2("p", { className: "studio-empty", children: say(locale, "emptyBand") })
        ] })
      }
    ),
    column.hasMore && column.closedSlice && /* @__PURE__ */ jsxs2("div", { className: "studio-more", "data-testid": "closed-slice", children: [
      /* @__PURE__ */ jsx2("span", { children: say(locale, "closedPartial", { loaded: column.closedSlice.loaded, total: column.closedSlice.total }) }),
      loadError && /* @__PURE__ */ jsx2("span", { role: "alert", className: "studio-error", children: loadError }),
      /* @__PURE__ */ jsx2(
        "button",
        {
          type: "button",
          className: "studio-act",
          disabled: busy || column.loadingMore,
          onClick: () => {
            setBusy(true);
            setLoadError(null);
            actions.loadMore().then(() => setBusy(false), (e) => {
              setBusy(false);
              setLoadError(e instanceof Error ? e.message : String(e));
            });
          },
          children: busy || column.loadingMore ? say(locale, "loading") : say(locale, "loadMore")
        }
      )
    ] })
  ] });
};

// src/dashboard.tsx
import { useTasks } from "@entire-vc/mesh-theme-sdk";
import { TaskLink as TaskLink2 } from "@entire-vc/mesh-theme-sdk";
import { useSlotContext } from "@entire-vc/mesh-theme-sdk";
import { Fragment as Fragment3, jsx as jsx3, jsxs as jsxs3 } from "react/jsx-runtime";
var FOCUS_WIDGET = {
  id: "board-studio.focus",
  title: "Due this week",
  description: "Your overdue tasks and the ones due soon",
  origin: "theme",
  dataHook: "useTasks",
  actions: [],
  size: { default: { w: 2, h: 2 }, min: { w: 1, h: 1 }, max: { w: 4, h: 3 } }
};
var total = (items, days) => {
  const f = focus(items, days);
  return f.overdue.length + f.soon.length;
};
var DashLayout = ({ data, ctx }) => {
  const { Widget } = data.components;
  const locale = useSlotContext().locale;
  return /* @__PURE__ */ jsxs3("section", { className: "studio-dash", "aria-label": say(locale, "dashTitle"), children: [
    /* @__PURE__ */ jsx3("h1", { className: "studio-dash-title", children: say(locale, "dashTitle") }),
    data.widgets.length === 0 ? /* @__PURE__ */ jsx3("p", { className: "studio-empty-board", children: say(locale, "noWidgets") }) : /* @__PURE__ */ jsx3("div", { className: "studio-grid", "data-mobile": ctx.isMobile || void 0, children: data.widgets.map((w) => /* @__PURE__ */ jsx3("div", { className: "studio-cell", style: ctx.isMobile ? void 0 : { gridColumn: `span ${Math.min(w.descriptor.size.max.w, Math.max(w.descriptor.size.min.w, w.size.w))}` }, children: /* @__PURE__ */ jsx3(Widget, { id: w.descriptor.id, size: w.size }) }, w.descriptor.id)) })
  ] });
};
var FocusWidget = ({ settings }) => {
  const locale = useSlotContext().locale;
  const days = horizon(settings.focus_horizon_days);
  const r = useTasks({ scope: "mine", sort: "due_date", limit: 50 });
  return /* @__PURE__ */ jsxs3("section", { className: "studio-focus", "data-state": r.state, children: [
    /* @__PURE__ */ jsxs3("h2", { className: "studio-focus-title", children: [
      say(locale, "focusTitle"),
      r.state === "ready" && total(r.value.items, days) > 0 && /* @__PURE__ */ jsx3("b", { children: total(r.value.items, days) })
    ] }),
    r.state === "loading" && /* @__PURE__ */ jsx3("p", { role: "status", className: "studio-note", children: say(locale, "loading") }),
    r.state === "error" && /* @__PURE__ */ jsxs3("div", { role: "alert", className: "studio-alert", children: [
      /* @__PURE__ */ jsx3("span", { children: say(locale, "focusError") }),
      /* @__PURE__ */ jsx3("button", { type: "button", className: "studio-act", onClick: () => r.retry(), children: say(locale, "retry") })
    ] }),
    r.state === "ready" && /* @__PURE__ */ jsx3(FocusList, { locale, days, items: r.value.items, projectNames: r.value.projectNames })
  ] });
};
function FocusList({ locale, days, items, projectNames }) {
  const { overdue, soon } = focus(items, days);
  if (overdue.length + soon.length === 0) return /* @__PURE__ */ jsx3("p", { className: "studio-note", children: say(locale, "focusEmpty", { days }) });
  const group = (key, list, tone) => list.length === 0 ? null : /* @__PURE__ */ jsxs3("div", { className: "studio-focus-group", "data-tone": tone, children: [
    /* @__PURE__ */ jsxs3("h3", { children: [
      say(locale, key),
      " ",
      /* @__PURE__ */ jsx3("b", { children: list.length })
    ] }),
    /* @__PURE__ */ jsx3("ul", { children: list.map((t) => /* @__PURE__ */ jsxs3("li", { children: [
      /* @__PURE__ */ jsx3(TaskLink2, { taskId: t.id, children: t.title }),
      /* @__PURE__ */ jsx3("span", { className: "studio-focus-date", children: t.due_date?.slice(0, 10) }),
      projectNames[t.project_id] && /* @__PURE__ */ jsx3("span", { className: "studio-focus-meta", children: projectNames[t.project_id] })
    ] }, t.id)) })
  ] });
  return /* @__PURE__ */ jsxs3(Fragment3, { children: [
    group("overdueGroup", overdue, "late"),
    group("soonGroup", soon, "soon")
  ] });
}

// src/layout.tsx
import { useMemo as useMemo2, useState as useState2 } from "react";
import { jsx as jsx4, jsxs as jsxs4 } from "react/jsx-runtime";
var byKeys = (all, keys) => {
  const map = new Map(all.map((f) => [f.key, f]));
  return keys.flatMap((k) => map.has(k) ? [map.get(k)] : []);
};
var Layout = ({ data, actions, themeSettings, ctx }) => {
  const { columns, components, dnd, emptyReason, closedPartial } = data;
  const locale = ctx.locale;
  const [focus2, setFocus] = useState2(null);
  const fields = useMemo2(() => byKeys(data.availableFields, data.cardFields), [data.availableFields, data.cardFields]);
  const list = columns.state === "loading" ? null : columns.value ?? null;
  const people = useMemo2(() => lanes((list ?? []).flatMap((c) => c.tasks)), [list]);
  const settings = useMemo2(() => ({ ...themeSettings, studio_focus: focus2 }), [themeSettings, focus2]);
  if (columns.state === "loading") {
    return /* @__PURE__ */ jsx4("div", { className: "studio-board", "data-density": themeSettings.density, "aria-busy": "true", role: "status", "aria-label": say(locale, "loading"), children: [0, 1, 2].map((i) => /* @__PURE__ */ jsxs4("div", { className: "studio-band studio-skeleton", children: [
      /* @__PURE__ */ jsx4("div", { className: "studio-rail" }),
      /* @__PURE__ */ jsxs4("div", { className: "studio-drop", children: [
        /* @__PURE__ */ jsx4("div", { className: "studio-ghost" }),
        /* @__PURE__ */ jsx4("div", { className: "studio-ghost" })
      ] })
    ] }, i)) });
  }
  return /* @__PURE__ */ jsxs4("div", { className: "studio-board", "data-density": themeSettings.density, children: [
    columns.state === "error" && /* @__PURE__ */ jsxs4("div", { role: "alert", className: "studio-alert", children: [
      /* @__PURE__ */ jsx4("span", { children: columns.error.message }),
      /* @__PURE__ */ jsx4("button", { type: "button", className: "studio-act", onClick: () => columns.retry(), children: say(locale, "retry") })
    ] }),
    columns.state === "refreshing" && /* @__PURE__ */ jsx4("p", { role: "status", className: "studio-note", children: say(locale, "refreshing") }),
    closedPartial && list && /* @__PURE__ */ jsx4("p", { role: "status", className: "studio-note", "data-testid": "closed-partial-note", children: say(locale, "closedPartial", { loaded: closedPartial.loaded, total: closedPartial.total }) }),
    people.length > 1 && /* @__PURE__ */ jsxs4("div", { className: "studio-people", role: "group", "aria-label": say(locale, "focusOn"), children: [
      /* @__PURE__ */ jsx4("button", { type: "button", className: "studio-chip", "aria-pressed": focus2 === null, onClick: () => setFocus(null), children: say(locale, "everyone") }),
      people.map((p) => /* @__PURE__ */ jsxs4("button", { type: "button", className: "studio-chip", "aria-pressed": focus2 === p.key, onClick: () => setFocus(focus2 === p.key ? null : p.key), children: [
        p.name || say(locale, "unassigned"),
        " ",
        /* @__PURE__ */ jsx4("b", { children: p.count })
      ] }, p.key))
    ] }),
    list?.map((column, index) => /* @__PURE__ */ jsx4(
      components.Column,
      {
        data: { column, index, total: list.length, dnd, fields, components: { Card: components.Card } },
        actions: {
          createTask: column.status && actions.createTask ? () => actions.createTask?.(column.id) : void 0,
          loadMore: () => actions.loadMore(column.id),
          openTask: actions.openTask,
          quickEdit: actions.quickEdit,
          moveTask: (taskId, position) => actions.moveTask(taskId, column.id, position)
        },
        themeSettings: settings,
        ctx
      },
      column.id
    )),
    list && list.length === 0 && /* @__PURE__ */ jsx4("p", { className: "studio-empty-board", children: say(locale, emptyReason === "no_statuses" ? "noStatuses" : "noMatches") })
  ] });
};

// src/index.tsx
var src_default = {
  views: {
    "board.layout": { contract: "^1", view: Layout },
    "board.column": { contract: "^1", view: Column },
    "board.card": { contract: "^1", view: Card },
    "dashboard.layout": { contract: "^1", view: DashLayout },
    "dashboard.widget.focus": { contract: "^1", view: FocusWidget }
  },
  // The widgets this theme adds to the dashboard; the core lists them next to its own and draws them with the view above.
  widgets: [FOCUS_WIDGET]
};
export {
  src_default as default
};
