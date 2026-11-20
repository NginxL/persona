// The card an Agent the conversation handed work to is drawn as (#5096): cindy's SessionHandoffCard in Persona's tokens.
// One card per Agent a dispatch started or a task took over, in the chat where it happened; a click opens that Agent's
// own page (lib/handoff.ts), never a panel. Before its Agent is up the card stands for the task: starting, or why it
// did not start, in plain words (lib/start-failure.ts; the task's Needs you row has Retry and Remove). It is the task's
// only card in the chat (#8237): a turn the model service cut off shows on it too, in place, with why.
import { openAgent, openTask } from "../../lib/handoff";
import { startFailure, type TaskPlan } from "../../lib/feed";
import { startFailureCardLine } from "../../lib/start-failure";
import { interruption, TODO_STATUS_INFO, type Todo, type TodoAgent } from "../../lib/todos";
import { ago, stateOf } from "../../lib/format";
import { stateName } from "../../lib/state-labels";
import { useStore, type State } from "../../lib/store";
import { PHONE, useMedia } from "../../lib/viewport";
import type { Session } from "../../lib/types";
import { cx, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { StatusMark } from "../../ui/StatusMark";
import { t } from "../../i18n";

/** How the Agent came to the task (a `task` feed item's `wake`): started for it (New), running already when the task
 *  took it over (Already active), or closed and resumed by the hand-off's message (Resumed). */
export type HandoffWake = "created" | "active" | "resumed";

/** A `task` feed item's wake; anything else (none, an older hub) is New. */
export function feedWake(wake: string | undefined): HandoffWake {
  return wake === "active" || wake === "resumed" ? wake : "created";
}

/** Why a task's Agent did not start, from the hub's words on the to-do, its plan or the Agent's row. */
export function whyNotStarted(todo: Todo, plans: Record<string, TaskPlan>): string | undefined {
  return startFailure(todo, plans) ?? todo.agents?.find((a) => a.start_error)?.start_error;
}

/** How long after a hand-off's first Agent another counts as handed off with it (the plan's several Agents, a retry): one
 *  linked later (a new Agent after every one stopped, #4754; a plan that continues the to-do, #11854) has its own card
 *  where that happened. */
const SAME_HANDOFF_MS = 120_000;
/** How long before a standalone task line an Agent may be linked and still be that line's: the line is written just
 *  after the link (agent_start, a takeover). A plan line takes none: it is written before its Agents start, so an Agent
 *  linked before it is an earlier hand-off's (a previous continuation's, however recent). */
export const TASK_LINE_SLACK_MS = 30_000;

/** The Agents a card stands for (#11854). `since`: the time of the card's feed line, which anchors it to the first
 *  Agents linked from then on, so a card under an older message keeps the Agents that started then and one under the
 *  newest plan has the plan's own, never an older one while its Agent is starting (none, and the card says so).
 *  Without an anchor (or with nothing linked since, for a task that took over a running Agent or a plan a later one
 *  continued): the newest batch still linked, else the Agent released last. `slack`: how long before `since` an Agent
 *  may be linked and still count (TASK_LINE_SLACK_MS for a task line; none for a plan line, so a previous
 *  continuation's Agent linked just before the plan never stands in for the plan's own). */
export function handoffAgents(agents: TodoAgent[], since?: string, fallback = true, slack = 0): TodoAgent[] {
  const started = agents.filter((a) => !a.start_error);
  const at = (a: TodoAgent) => Date.parse(a.linked_at);
  const anchor = since ? Date.parse(since) : NaN;
  if (Number.isFinite(anchor) && started.every((a) => Number.isFinite(at(a)))) {
    const after = started.filter((a) => at(a) >= anchor - slack);
    if (after.length > 0) {
      const first = Math.min(...after.map(at));
      return after.filter((a) => at(a) - first <= SAME_HANDOFF_MS);
    }
    if (!fallback) return [];
  }
  const time = (a: TodoAgent) => Date.parse(a.linked_at) || 0;
  const current = started.filter((a) => !a.released_at);
  if (current.length > 0) {
    const last = Math.max(...current.map(time));
    return current.filter((a) => last - time(a) <= SAME_HANDOFF_MS);
  }
  const released = (a: TodoAgent) => Date.parse(a.released_at ?? "") || time(a);
  const latest = started.reduce<TodoAgent | undefined>((best, a) => (!best || released(a) > released(best) ? a : best), undefined);
  return latest ? [latest] : [];
}

/** What one card stands for; flat, so a list of them built by a store selector compares field by field. */
export interface Handoff {
  /** Unique among the cards of one feed line. */
  key: string;
  /** The task's title: the card's until its Agent's session has one. */
  title: string;
  wake: HandoffWake;
  /** The Agent's computer and session; none while the task has no Agent yet. */
  mid?: string;
  sid?: string;
  /** What the to-do calls its Agent, before the session list has it. */
  agentTitle?: string;
  /** The to-do the task is, once there is one. */
  todo?: string;
  /** Why the task did not start (no Agent), as the hub said it; the card says it in plain words. */
  failure?: string;
  /** The task has no Agent and never will: closed before one started, or removed since (no `todo`). */
  ended?: boolean;
}

/** A task's cards: one per Agent the hand-off went to (handoffAgents), or one for the task while it has none: starting,
 *  why it did not start, or gone (removed since). `planned`: the plan's task, for its title and error before the to-do
 *  arrives (or after it was removed); `superseded`: a later plan continues its to-do, so with no Agent of its own the
 *  card shows the to-do's latest rather than starting for ever. `loaded`: the to-do list is in, so a to-do it lacks is
 *  gone rather than on its way. `since`: the card's feed line's time; `slack`: how long before it its Agents may be linked
 *  (TASK_LINE_SLACK_MS for a standalone task line, none for a plan's). */
export function taskHandoffs(todo: Todo | undefined, planned: { key: string; title: string; error?: string; todoId?: string; superseded?: boolean } | undefined, wake: HandoffWake, plans: Record<string, TaskPlan>, loaded = true, since?: string, slack = 0): Handoff[] {
  // a task made of a running Agent, or resumed: its Agent may be linked before the line; one a plan started never is
  const shown = handoffAgents(todo?.agents ?? [], since, wake !== "created" || Boolean(planned?.superseded), slack);
  if (todo && shown.length > 0) {
    return shown.map((a) => ({ key: `${a.machine_id}/${a.session_id}`, title: todo.title, wake, mid: a.machine_id, sid: a.session_id, agentTitle: a.title, todo: todo.id }));
  }
  const failure = ((todo ? whyNotStarted(todo, plans) : undefined) ?? planned?.error)?.trim();
  const ended = !failure && (todo ? Boolean(todo.done_at) : Boolean(planned?.todoId) && loaded);
  return [{ key: todo ? `todo:${todo.id}` : (planned?.key ?? "task"), title: todo?.title ?? planned?.title ?? "", wake, todo: todo?.id, failure: failure || undefined, ended: ended || undefined }];
}

/** cindy's short id: the first 8 and last 4 characters ("83639512…7ed0"). Only in the card's tooltip: the Assistant's
 *  chat keeps ids out of sight (super rule 4). */
export function shortSessionId(id: string): string {
  return id.length <= 13 ? id : `${id.slice(0, 8)}…${id.slice(-4)}`;
}

/** The Agent's session as the store has it now. */
function sessionIn(s: State, mid: string, sid: string): Session | undefined {
  return (s.sessions[mid] ?? []).find((x) => x.session_id === sid) ?? s.sessionSummaries.find((x) => x.machine_id === mid && x.session_id === sid);
}

/** "last active 3 minutes ago", from the Agent's latest step or update; undefined without either. `compact`: a phone's
 *  narrow card, where the time alone follows the history mark ("3 min. ago"). */
export function lastActive(session: Pick<Session, "activity_at" | "updated_at"> | undefined, now = Date.now(), compact = false): string | undefined {
  const at = session?.activity_at || session?.updated_at;
  const ts = at ? Date.parse(at) : NaN;
  if (!Number.isFinite(ts)) return undefined;
  if (compact) return ago(at, now, "short") || undefined;
  if (now - ts < 45_000) return t("assistant-tasks.handoff_active_now");
  const when = ago(at, now);
  return when ? t("assistant-tasks.handoff_last_active", { when }) : undefined;
}

export function HandoffCard({ handoff }: { handoff: Handoff }) {
  const { mid, sid, todo, failure, wake } = handoff;
  const agent = mid && sid ? { mid, sid } : undefined;
  const session = useStore((s) => (agent ? sessionIn(s, agent.mid, agent.sid) : undefined));
  const closed = useStore((s) => Boolean(todo && s.todos.find((x) => x.id === todo)?.done_at));
  useNow(60_000, Boolean(agent)); // "last active" moves on by the minute
  const title = session?.title?.trim() || handoff.agentTitle?.trim() || handoff.title || t("assistant-tasks.task_fallback");
  const badge = wake === "active" ? t("assistant-tasks.handoff_wake_active") : wake === "resumed" ? t("assistant-tasks.handoff_wake_resumed") : t("assistant-tasks.handoff_wake_created");
  const interactive = Boolean(agent || todo);
  const open = () => {
    if (agent) openAgent(agent.mid, agent.sid);
    else if (todo) openTask(todo);
  };
  const state = session ? stateOf(session) : undefined;
  // a turn the model service cut off, while the task is open: the card says so with why (the row in Needs you continues it)
  const cut = !closed ? interruption(session) : undefined;
  const phone = useMedia(PHONE);
  const active = lastActive(session, Date.now(), phone);
  const meta = agent ? (
    <>
      {active ? (
        <>
          <Icon name="history" size={12} className="shrink-0 text-ink-2" />
          <span className="truncate text-[12px] leading-4 text-ink-2">{active}</span>
        </>
      ) : (
        <span className="truncate text-[12px] leading-4 text-ink-3">{t("assistant-tasks.handoff_not_active")}</span>
      )}
      {(cut || state) && <span aria-hidden className="shrink-0 text-[12px] leading-4 text-ink-3">·</span>}
      {cut ? (
        <span data-handoff-interrupted className="inline-flex min-w-0 items-center gap-1 text-xs text-red">
          <Icon name="info" size={12} className="shrink-0" />
          <span className="truncate">{`${TODO_STATUS_INFO.interrupted.label} · ${cut.reason}`}</span>
        </span>
      ) : state && (
        <span data-handoff-state={state} className={cx("inline-flex shrink-0 items-center gap-1 text-[12px] leading-4", state === "needs_you" ? "text-attention" : "text-ink-2")}>
          <StatusMark state={state} size="sm" label={false} />
          {stateName(state)}
        </span>
      )}
    </>
  ) : failure ? (
    <span data-handoff-failure className="truncate text-[12px] leading-4 text-red">{startFailureCardLine(failure)}</span>
  ) : handoff.ended ? (
    <span className="truncate text-[12px] leading-4 text-ink-3">{todo ? t("assistant-tasks.handoff_closed") : t("assistant-tasks.handoff_gone")}</span>
  ) : (
    <span className="truncate text-[12px] leading-4 text-ink-3">{t("assistant-tasks.starting_agent")}</span>
  );
  const body = (
    <>
      <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-field sm:size-9 transition-colors duration-150 group-hover:bg-ink">
        <Icon name="cornerDownRight" size={18} stroke={2} className="text-ink transition-colors duration-150 group-hover:text-surface" />
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 truncate text-sm leading-5 font-semibold text-ink">{title}</span>
          <span
            data-handoff-wake={wake}
            className={cx(
              "shrink-0 rounded-full px-2 py-[2px] text-[10px] leading-4 font-medium tracking-[0.4px] whitespace-nowrap uppercase",
              // cindy: New and Resumed filled (just started or woken), Already active outlined
              wake === "active" ? "border border-ink bg-surface text-ink" : "bg-ink text-surface",
            )}
          >
            {badge}
          </span>
        </span>
        <span className="flex min-w-0 items-center gap-1.5">{meta}</span>
      </span>
      {interactive && (
        <span className="flex size-6 shrink-0 items-center justify-center">
          <Icon name="arrowUpRight" size={16} stroke={2} className="text-ink-2 transition-colors duration-150 group-hover:text-ink" />
        </span>
      )}
    </>
  );
  // cindy's 16 px padding and 14 px gap; a phone's narrower card gives them back to the words
  const look = "flex w-full min-w-0 items-center gap-3 rounded-xl border border-line bg-surface p-3 text-left sm:gap-3.5 sm:p-4";
  const data = { "data-handoff-card": agent ? `${agent.mid}/${agent.sid}` : todo ? `todo:${todo}` : handoff.key };
  if (!interactive) return <div {...data} className={look}>{body}</div>;
  return (
    <button
      type="button"
      {...data}
      title={agent ? `${title} · ${shortSessionId(agent.sid)}` : title}
      onClick={open}
      className={cx(look, "group transition-colors duration-150 hover:border-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ink focus-visible:ring-offset-2 focus-visible:ring-offset-canvas")}
    >
      {body}
    </button>
  );
}

/** The cards of one feed line, stacked as cindy stacks them: one per Agent, no heading over them. */
export function HandoffCards({ handoffs }: { handoffs: Handoff[] }) {
  if (handoffs.length === 0) return null;
  return (
    <div data-handoff-cards={handoffs.length} className="flex w-full min-w-0 flex-col gap-2">
      {handoffs.map((h) => <HandoffCard key={h.key} handoff={h} />)}
    </div>
  );
}
