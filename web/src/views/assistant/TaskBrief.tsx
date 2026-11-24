// A task's brief where the owner meets the task (#3822, docs/assistant/unified-entry.md "Briefs"): on the plan card
// before "Start" (their own words with the date and one line that counts the rest; the goal, the criteria, the
// expected time and what is out of bounds open on demand, so the card's buttons stay in reach), on
// the task's page above its criteria (a brief made later says so and shows the hand-over prompt as that), under them
// the scope changes and who worked on it, and under a finished task's criteria what their list cannot say (the
// criteria a scope change dropped, what was done beyond the brief). None of it is on
// the first screen: every user is a lazy import, and the strings are the namespace `assistant-taskbrief`, which loads
// with the rest of the catalog.
import { useEffect, useState, type ReactNode } from "react";
import { formatDate, formatNumber, t } from "../../i18n";
import { briefAsked, briefCriteria, briefExpect, lineageTells, scopeChanges, type BriefCriterion, type LineageStep, type TaskBrief } from "../../lib/brief";
import { useStore } from "../../lib/store";
import type { Todo } from "../../lib/todos";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useClamped } from "../../ui/use-clamped";

const CLAMP = { 2: "line-clamp-2", 3: "line-clamp-3", 4: "line-clamp-4" } as const;

const TOGGLE = "touch-hit -ml-1 w-fit rounded-control px-1 text-[12px] font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2";

/** Whether the box around a clamp opens it (the plan card's one disclosure), and where it hears that the text is cut. */
type Held = { open: boolean; onCut: (cut: boolean) => void };

/** Text held to a few lines; when they cut it, "Show all" / "Show less" under it opens the rest in place. `held`: the
 *  box around it has the one control, and this draws none. */
function Clamp({ lines, className, children, held }: { lines: keyof typeof CLAMP; className?: string; children: ReactNode; held?: Held }) {
  const [own, setOpen] = useState(false);
  const open = held ? held.open : own;
  const { ref, cut } = useClamped<HTMLDivElement>(open);
  const onCut = held?.onCut;
  useEffect(() => onCut?.(cut), [cut, onCut]);
  return (
    <>
      <div ref={ref} data-brief-clamp={open ? "open" : ""} className={cx("whitespace-pre-wrap select-text [overflow-wrap:anywhere]", className, !open && CLAMP[lines])}>
        {children}
      </div>
      {cut && !held && (
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className={TOGGLE}>
          {t(open ? "assistant-taskbrief.show_less" : "assistant-taskbrief.show_all")}
        </button>
      )}
    </>
  );
}

/** "About 30 min", "About 2.5 h". */
export function expectText(minutes: number): string {
  if (minutes < 120) return t("assistant-taskbrief.expect_minutes", { minutes });
  return t("assistant-taskbrief.expect_hours", { hours: formatNumber(Math.round(minutes / 6) / 10, { maximumFractionDigits: 1 }) });
}

/** Who asked: the owner's own words under "You asked on <date>:", who started it and why when it was not them, or
 *  that the brief was made later, with the hand-over prompt labelled as that. */
function Asked({ brief, lines, held }: { brief: TaskBrief; lines: keyof typeof CLAMP; held?: Held }) {
  const asked = briefAsked(brief);
  if (asked.kind === "owner") {
    return (
      <div data-brief-asked="owner" className="flex min-w-0 gap-2">
        <Icon name="quote" size={13} className="mt-0.5 shrink-0 text-ink-3" />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <Clamp lines={lines} className="text-ink" held={held}>
            <span className="mr-1 text-ink-2">{t("assistant-taskbrief.asked_owner", { date: formatDate(asked.at) })}</span>
            {asked.text}
          </Clamp>
          {asked.truncated && <span className="text-[12px] text-ink-3">{t("assistant-taskbrief.asked_cut")}</span>}
        </div>
      </div>
    );
  }
  if (asked.kind === "reconstructed") {
    return (
      <div data-brief-asked="reconstructed" className="flex min-w-0 flex-col gap-1.5">
        <div className="flex min-w-0 gap-2 text-ink-2">
          <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
          <span className="min-w-0">{t("assistant-taskbrief.reconstructed")}</span>
        </div>
        {brief.handover?.trim() && (
          <div className="flex min-w-0 flex-col gap-0.5 pl-[21px]">
            <span className="text-[12px] font-medium text-ink-3">{t("assistant-taskbrief.handover")}</span>
            <Clamp lines={lines} className="text-ink-2" held={held}>{brief.handover.trim()}</Clamp>
          </div>
        )}
      </div>
    );
  }
  const line = asked.kind === "started" ? t(`assistant-taskbrief.started_${asked.by}`, { date: formatDate(asked.at) }) : t("assistant-taskbrief.started_unknown", { date: formatDate(asked.at) });
  const why = asked.kind === "started" ? asked.why : "";
  return (
    <div data-brief-asked={asked.kind === "started" ? asked.by : "unknown"} className="flex min-w-0 gap-2 text-ink-2">
      <Icon name={asked.kind === "started" ? "spark" : "info"} size={13} className="mt-0.5 shrink-0 text-ink-3" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <Clamp lines={lines} held={held}>{why ? t("assistant-taskbrief.started_why", { line, why }) : line}</Clamp>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="whitespace-nowrap text-ink-3">{label}</dt>
      <dd className="min-w-0 text-ink-2 select-text [overflow-wrap:anywhere]">{children}</dd>
    </>
  );
}

function Lines({ items }: { items: string[] }) {
  if (items.length === 1) return <>{items[0]}</>;
  return (
    <ul className="flex list-disc flex-col gap-0.5 pl-4">
      {items.map((x, i) => (
        <li key={i}>{x}</li>
      ))}
    </ul>
  );
}

/** The brief's fields as label / value rows: the goal (when it says more than the title above it), the criteria,
 *  the expected time, what is out of bounds. */
function Fields({ brief, title, criteria, expect }: { brief: TaskBrief; title: string; criteria: boolean; expect: boolean }) {
  const goal = brief.goal?.trim();
  const list = criteria ? briefCriteria(brief) : [];
  const minutes = expect ? briefExpect(brief) : 0;
  const bounds = brief.out_of_bounds ?? [];
  const showGoal = !!goal && goal !== title.trim();
  if (!showGoal && !list.length && !minutes && !bounds.length) return null;
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
      {showGoal && <Field label={t("assistant-taskbrief.goal")}>{goal}</Field>}
      {list.length > 0 && (
        <Field label={t("assistant-taskbrief.done_when")}>
          <Lines items={list} />
        </Field>
      )}
      {minutes > 0 && <Field label={t("assistant-taskbrief.expect")}>{expectText(minutes)}</Field>}
      {bounds.length > 0 && (
        <Field label={t("assistant-taskbrief.out_of_bounds")}>
          <Lines items={bounds} />
        </Field>
      )}
    </dl>
  );
}

/** "2 criteria · about 45 min · 1 out of bounds": what a plan card's folded brief holds, in one line. */
export function briefSummary(brief: TaskBrief): string {
  const criteria = briefCriteria(brief).length;
  const minutes = briefExpect(brief);
  const bounds = brief.out_of_bounds?.length ?? 0;
  return [
    criteria > 0 && t("assistant-taskbrief.summary_criteria", { count: criteria }),
    minutes > 0 && (minutes < 120 ? t("assistant-taskbrief.summary_minutes", { minutes }) : t("assistant-taskbrief.summary_hours", { hours: formatNumber(Math.round(minutes / 6) / 10, { maximumFractionDigits: 1 }) })),
    bounds > 0 && t("assistant-taskbrief.summary_bounds", { count: bounds }),
  ].filter(Boolean).join(t("assistant-taskbrief.separator"));
}

/** A plan task's brief on the plan card, before "Start" confirms it. Folded, it is who asked (two lines) and one line
 *  that counts the rest, so the card's own buttons stay in reach; "Show all", its one control, opens the whole request
 *  and the goal, the criteria, the expected time and what is out of bounds in place. */
export function PlanBrief({ brief, title }: { brief: TaskBrief; title: string }) {
  const [open, setOpen] = useState(false);
  const [cut, setCut] = useState(false);
  const summary = briefSummary(brief);
  const goal = brief.goal?.trim();
  const more = cut || open || !!summary || (!!goal && goal !== title.trim());
  return (
    <div data-plan-brief={open ? "open" : ""} className="flex min-w-0 flex-col gap-1 rounded-control bg-inset px-3 py-1.5 text-[12.5px] leading-snug shadow-hairline">
      <Asked brief={brief} lines={2} held={{ open, onCut: setCut }} />
      {open && <Fields brief={brief} title={title} criteria expect />}
      {more && (
        <div className="flex min-w-0 items-center gap-2">
          {!open && summary && <span data-brief-summary className="min-w-0 truncate text-ink-3">{summary}</span>}
          <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className={cx(TOGGLE, "shrink-0")}>
            {t(open ? "assistant-taskbrief.show_less" : "assistant-taskbrief.show_all")}
          </button>
        </div>
      )}
    </div>
  );
}

/** The brief on the task's page, above its criteria: who asked and in what words, the goal, what is out of bounds,
 *  and a mark once it ran past its clock. The criteria and the expected time are in the review box under it when the
 *  task has one; a task without one lists them here. */
export function TaskBriefPanel({ todo }: { todo: Todo }) {
  const brief = todo.brief;
  if (!brief) return null;
  const overdue = !todo.done_at && brief.overdue_at ? formatDate(brief.overdue_at) : "";
  const reviewed = !!todo.supervision;
  return (
    <section data-testid="task-brief" data-brief-overdue={overdue ? "" : undefined} className="mt-4 flex flex-col gap-2 rounded-control bg-inset p-3.5 text-[13px] leading-snug shadow-hairline">
      <div className="flex min-w-0 items-center gap-2">
        <h3 className="text-[12.5px] font-semibold text-ink-2">{t("assistant-taskbrief.title")}</h3>
        {overdue && (
          <span data-testid="brief-overdue" className="inline-flex h-5 shrink-0 items-center gap-1 whitespace-nowrap rounded-md bg-orange-tint px-1.5 text-[11.5px] font-semibold text-orange">
            <Icon name="clock" size={11} stroke={2.2} />
            {t("assistant-taskbrief.overdue")}
          </span>
        )}
      </div>
      <Asked brief={brief} lines={4} />
      <Fields brief={brief} title={todo.title} criteria={!reviewed} expect={!reviewed || !todo.supervision?.expect_minutes} />
      {overdue && <p className="text-[12px] text-ink-2">{t("assistant-taskbrief.overdue_since", { date: overdue })}</p>}
    </section>
  );
}

/** Under a finished task's criteria (the review box's own list says met, missing and not checked, #3635): the
 *  criteria a scope change dropped, struck through with the change that dropped them, then what was done beyond the
 *  brief. In a mark and in words, never by color alone. */
export function BriefResultExtras({ dropped, alsoDone }: { dropped: BriefCriterion[]; alsoDone: string[] }) {
  if (!dropped.length && !alsoDone.length) return null;
  return (
    <div data-testid="brief-result" className="mt-1.5 flex min-w-0 flex-col gap-1.5 text-[13px] leading-snug">
      {dropped.length > 0 && (
        <ul className="flex flex-col gap-1.5">
          {dropped.map((c, i) => (
            <li key={i} data-criterion-state="dropped" className="flex items-start gap-2">
              <span aria-hidden className="mt-0.5 shrink-0 text-ink-3">
                <Icon name="undo" size={13} stroke={2.4} />
              </span>
              <span className="flex min-w-0 flex-1 flex-col select-text [overflow-wrap:anywhere]">
                <span className="text-ink-2 line-through">{c.criterion}</span>
                <span className="text-[12px] text-ink-3">
                  {c.change ? t("assistant-taskbrief.state_detail", { state: t("assistant-taskbrief.state_dropped"), detail: c.change }) : t("assistant-taskbrief.state_dropped")}
                </span>
              </span>
            </li>
          ))}
        </ul>
      )}
      {alsoDone.length > 0 && (
        <div data-testid="brief-also-done" className="flex min-w-0 flex-col gap-0.5 text-[12.5px] text-ink-2 select-text [overflow-wrap:anywhere]">
          <span className="font-medium text-ink-3">{t("assistant-taskbrief.also_done")}</span>
          <Lines items={alsoDone} />
        </div>
      )}
    </div>
  );
}

/** A lineage step's line under its title: who and where, then from when to when. */
function StepLine({ step, machine }: { step: LineageStep; machine: string }) {
  const who = step.agent && machine ? t("assistant-taskbrief.lineage_on", { agent: step.agent, machine }) : step.agent || machine;
  const when = step.ended ? t("assistant-taskbrief.lineage_period", { from: formatDate(step.started), to: formatDate(step.ended) }) : t("assistant-taskbrief.lineage_since", { from: formatDate(step.started) });
  return <span className="text-[12px] text-ink-3">{[who, when].filter(Boolean).join(t("assistant-taskbrief.separator"))}</span>;
}

/** One direction of the hand-off, linked to its card when the detail page supplied navigation (#5732). */
function LineageLink({ id, label, onOpenTodo }: { id: string; label: string; onOpenTodo?: (id: string) => void }) {
  if (!onOpenTodo) return <span className="text-xs text-ink-2">{label}</span>;
  return (
    <button type="button" data-open-todo={id} onClick={() => onOpenTodo(id)} className="w-fit whitespace-nowrap text-left text-xs text-accent-ink hover:underline">
      {label}
    </button>
  );
}

/** Under the task's criteria: the scope changes (what changed, why, what it added and dropped) and who worked on it
 *  (each to-do of the brief's lineage: where, from when to when, why it stopped). Nothing for a task whose scope
 *  never changed and that nobody took over. */
export function TaskBriefHistory({ todo, onOpenTodo }: { todo: Todo; onOpenTodo?: (id: string) => void }) {
  const machines = useStore((s) => s.machines);
  const changes = scopeChanges(todo.brief);
  const lineage = lineageTells(todo.lineage) ? todo.lineage : [];
  if (!changes.length && !lineage.length) return null;
  const heading = "text-[12.5px] font-semibold text-ink-2";
  return (
    <section data-testid="task-brief-history" className="mt-4 flex flex-col gap-3 rounded-control bg-inset p-3.5 text-[13px] leading-snug shadow-hairline">
      {changes.length > 0 && (
        <div data-testid="brief-scope" className="flex min-w-0 flex-col gap-2">
          <h3 className={heading}>{t("assistant-taskbrief.scope_title")}</h3>
          <ul className="flex flex-col gap-2">
            {changes.map((e, i) => (
              <li key={i} className="flex min-w-0 items-start gap-2">
                <Icon name="pencil" size={13} className="mt-0.5 shrink-0 text-ink-3" />
                <div className="flex min-w-0 flex-1 flex-col gap-0.5 select-text [overflow-wrap:anywhere]">
                  <span className="text-ink">{e.change}</span>
                  {e.why && <span className="text-[12.5px] text-ink-2">{t("assistant-taskbrief.scope_why", { why: e.why })}</span>}
                  {e.added?.map((c) => (
                    <span key={`a${c}`} className="text-[12.5px] text-ink-2">{t("assistant-taskbrief.scope_added", { criterion: c })}</span>
                  ))}
                  {e.dropped?.map((c) => (
                    <span key={`d${c}`} className="text-[12.5px] text-ink-2">{t("assistant-taskbrief.scope_dropped", { criterion: c })}</span>
                  ))}
                  {e.expect_minutes ? <span className="text-[12.5px] text-ink-2">{t("assistant-taskbrief.scope_expect", { time: expectText(e.expect_minutes) })}</span> : null}
                  <span className="text-[12px] text-ink-3">
                    {[formatDate(e.at), e.approval && t("assistant-taskbrief.scope_approved")].filter(Boolean).join(t("assistant-taskbrief.separator"))}
                  </span>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
      {lineage.length > 0 && (
        <div data-testid="brief-lineage" className={cx("flex min-w-0 flex-col gap-2", changes.length > 0 && "border-t border-line pt-3")}>
          <h3 className={heading}>{t("assistant-taskbrief.lineage_title")}</h3>
          <ol className="flex flex-col gap-2">
            {lineage.map((step, i) => (
              <li key={step.todo_id} data-lineage-current={step.current || undefined} className="flex min-w-0 items-start gap-2">
                <span aria-hidden className="mt-px flex size-[18px] shrink-0 items-center justify-center rounded-full bg-field text-[11px] font-bold text-ink-2 tabular-nums">{i + 1}</span>
                <div className="flex min-w-0 flex-1 flex-col gap-0.5 select-text [overflow-wrap:anywhere]">
                  <span className="flex min-w-0 flex-wrap items-baseline gap-x-2">
                    {!step.current && onOpenTodo ? (
                      <button type="button" data-open-todo={step.todo_id} onClick={() => onOpenTodo(step.todo_id)} className="min-w-0 truncate text-left text-accent-ink hover:underline">
                        {step.title || t("assistant-taskbrief.lineage_untitled")}
                      </button>
                    ) : (
                      <span className={cx("min-w-0", step.current ? "font-semibold text-ink" : "text-ink")}>{step.title || t("assistant-taskbrief.lineage_untitled")}</span>
                    )}
                    {step.current && <span className="shrink-0 whitespace-nowrap text-[12px] text-ink-3">{t("assistant-taskbrief.lineage_current")}</span>}
                  </span>
                  <StepLine step={step} machine={machines.find((m) => m.id === step.machine)?.name ?? ""} />
                  {step.continues && (
                    <LineageLink id={step.continues} label={t("assistant-taskbrief.lineage_continues", { title: lineage.find((s) => s.todo_id === step.continues)?.title || step.continues })} onOpenTodo={onOpenTodo} />
                  )}
                  {step.superseded_by && (
                    <LineageLink id={step.superseded_by} label={t("assistant-taskbrief.lineage_superseded", { title: lineage.find((s) => s.todo_id === step.superseded_by)?.title || step.superseded_by })} onOpenTodo={onOpenTodo} />
                  )}
                  {step.why ? (
                    <span className="text-[12.5px] text-ink-2">{t("assistant-taskbrief.lineage_stopped", { why: step.why })}</span>
                  ) : !step.ended && !step.superseded_by ? (
                    <span className="text-[12.5px] text-ink-2">{t("assistant-taskbrief.lineage_going")}</span>
                  ) : null}
                </div>
              </li>
            ))}
          </ol>
        </div>
      )}
    </section>
  );
}
