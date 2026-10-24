import { useTasks } from "@entire-vc/mesh-theme-sdk";
import type { DashboardLayoutView, DashboardWidgetView, WidgetDescriptor } from "@entire-vc/mesh-theme-sdk";
import { focus as pickFocus, horizon, say } from "./logic";
import { TaskLink } from "@entire-vc/mesh-theme-sdk";
import { useSlotContext } from "@entire-vc/mesh-theme-sdk";

/** The widget this theme adds to the core's four. `<theme-id>.<name>`; its view is `dashboard.widget.focus`. */
export const FOCUS_WIDGET: WidgetDescriptor = {
  id: "board-studio.focus",
  title: "Due this week",
  description: "Your overdue tasks and the ones due soon",
  origin: "theme",
  dataHook: "useTasks",
  actions: [],
  size: { default: { w: 2, h: 2 }, min: { w: 1, h: 1 }, max: { w: 4, h: 3 } },
};

const total = (items: { id: string; title: string; due_date?: string | null; project_id: string }[], days: number) => {
  const f = pickFocus(items, days);
  return f.overdue.length + f.soon.length;
};

/** `dashboard.layout`: a twelve-column grid; a widget takes three columns per unit of width. */
export const DashLayout: DashboardLayoutView = ({ data, ctx }) => {
  const { Widget } = data.components;
  const locale = useSlotContext().locale;
  return (
    <section className="studio-dash" aria-label={say(locale, "dashTitle")}>
      <h1 className="studio-dash-title">{say(locale, "dashTitle")}</h1>
      {data.widgets.length === 0 ? (
        <p className="studio-empty-board">{say(locale, "noWidgets")}</p>
      ) : (
        <div className="studio-grid" data-mobile={ctx.isMobile || undefined}>
          {data.widgets.map((w) => (
            <div key={w.descriptor.id} className="studio-cell" style={ctx.isMobile ? undefined : { gridColumn: `span ${Math.min(w.descriptor.size.max.w, Math.max(w.descriptor.size.min.w, w.size.w))}` }}>
              <Widget id={w.descriptor.id} size={w.size} />
            </div>
          ))}
        </div>
      )}
    </section>
  );
};

/** `dashboard.widget.focus`: built from the SDK's `useTasks` only: the user's own tasks, so their rights apply. */
export const FocusWidget: DashboardWidgetView = ({ settings }) => {
  const locale = useSlotContext().locale;
  const days = horizon(settings.focus_horizon_days);
  const r = useTasks({ scope: "mine", sort: "due_date", limit: 50 });
  return (
    <section className="studio-focus" data-state={r.state}>
      <h2 className="studio-focus-title">{say(locale, "focusTitle")}{r.state === "ready" && total(r.value.items, days) > 0 && <b>{total(r.value.items, days)}</b>}</h2>
      {r.state === "loading" && <p role="status" className="studio-note">{say(locale, "loading")}</p>}
      {r.state === "error" && (
        <div role="alert" className="studio-alert">
          <span>{say(locale, "focusError")}</span>
          <button type="button" className="studio-act" onClick={() => r.retry()}>{say(locale, "retry")}</button>
        </div>
      )}
      {r.state === "ready" && <FocusList locale={locale} days={days} items={r.value.items} projectNames={r.value.projectNames} />}
    </section>
  );
};

function FocusList({ locale, days, items, projectNames }: { locale: string; days: number; items: { id: string; title: string; due_date?: string | null; project_id: string }[]; projectNames: Record<string, string> }) {
  const { overdue, soon } = pickFocus(items, days);
  if (overdue.length + soon.length === 0) return <p className="studio-note">{say(locale, "focusEmpty", { days })}</p>;
  const group = (key: string, list: typeof items, tone: "late" | "soon") =>
    list.length === 0 ? null : (
      <div className="studio-focus-group" data-tone={tone}>
        <h3>{say(locale, key)} <b>{list.length}</b></h3>
        <ul>
          {list.map((t) => (
            <li key={t.id}>
              <TaskLink taskId={t.id}>{t.title}</TaskLink>
              <span className="studio-focus-date">{t.due_date?.slice(0, 10)}</span>
              {projectNames[t.project_id] && <span className="studio-focus-meta">{projectNames[t.project_id]}</span>}
            </li>
          ))}
        </ul>
      </div>
    );
  return <>{group("overdueGroup", overdue, "late")}{group("soonGroup", soon, "soon")}</>;
}
