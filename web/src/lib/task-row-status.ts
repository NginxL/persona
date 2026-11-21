// A task row among the tasks in progress (#6492): its status column says only what its Agent itself is doing, drawn
// with the one mark set for Agent states (StatusMark); what the to-do itself is at (parked, waiting to merge, accepted…)
// is a separate chip. Before, one column mixed both, so a parked to-do whose Agent had stopped read "Parked" and an
// Agent's own state and a to-do's state were drawn with different icon sets.
import { t } from "../i18n";
import { foldedState } from "./child-sessions";
import { stateName, waitName, type WaitReason } from "./state-labels";
import { reviewState, type Supervision } from "./supervision";
import type { AgentState, Session } from "./types";

/** What the Agent of a task row is doing, as its status column names it. */
export type AgentRowKey = "working" | "waiting" | "needs_you" | "unread" | "idle" | "stopped" | "interrupted"
  | "usage_limit" | "failed" | "starting" | "not_started";

const MARK: Record<AgentRowKey, AgentState> = {
  working: "working", needs_you: "needs_you", unread: "unread", idle: "idle", waiting: "idle", interrupted: "idle",
  usage_limit: "idle", stopped: "offline", failed: "offline", starting: "working", not_started: "offline", // as #7594 draws them
};

/** The Agent's own state for a task row: live background work first, then the task's status where it is the Agent's
 *  (needs you, working, done not seen, waiting, interrupted, a usage limit, never started), then its lead session's own
 *  state for a task whose status is the to-do's (finished, follow up, parked): stopped when it is gone, idle when it is
 *  there and quiet. `todo` false: no to-do yet (a plan card), so nothing has started; `starting`: its start is in flight
 *  (startInFlight), so a to-do nobody dispatched reads Not started (#7594). */
export function agentRowKey(o: { live: boolean; status: string; lead?: Session; todo: boolean; starting: boolean }): AgentRowKey {
  if (o.live) return "working";
  switch (o.status) {
    case "needs_you": case "working": case "unread": case "waiting": case "interrupted": case "usage_limit": case "failed":
      return o.status;
    case "not_started":
      return o.todo && o.starting ? "starting" : "not_started"; // only while a start is in flight (#7594)
  }
  if (!o.todo) return "not_started";
  if (!o.lead) return "stopped";
  const own = foldedState(o.lead); // a working or waiting child session counts, as in todoStatus and mostUrgentAgent
  if (own === "offline") return "stopped";
  if (own === "working" || own === "needs_you" || own === "unread") return own;
  return "idle";
}

/** The mark an Agent row key is drawn with (StatusMark's states). */
export function agentRowMark(key: AgentRowKey): AgentState {
  return MARK[key];
}

/** The status column's words for an Agent row key; a waiting one's say what it waits on (`wait`, lib/todo-wait-reason.ts
 *  waitReason, #11090; none reads as Paused, nothing needed from you). */
export function agentRowLabel(key: AgentRowKey, wait?: WaitReason): string {
  switch (key) {
    case "working": case "needs_you": case "unread": case "interrupted": case "failed":
      return stateName(key);
    case "waiting": return waitName(wait ?? "unknown");
    case "usage_limit": return t("lib-usage-limit.short");
    case "idle": return t("assistant-tasks.agent_idle");
    case "stopped": return t("assistant-tasks.agent_stopped");
    case "starting": return t("assistant-tasks.status_starting");
    case "not_started": return stateName("not_started");
  }
}

/** The colour of the status column's words: Needs you and a wait on the owner (Waiting on you, #11090) attention, a
 *  usage limit orange, a cut or failed one red. */
export function agentRowTone(key: AgentRowKey, wait?: WaitReason): string {
  if (key === "needs_you" || (key === "waiting" && wait === "owner")) return "text-attention";
  if (key === "usage_limit") return "text-orange";
  if (key === "interrupted" || key === "failed") return "text-red";
  return "text-ink-2";
}

/** The to-do's own state for its chip, undefined when the to-do has nothing to say beyond its Agent: waiting to start
 *  (a plan card), accepted, cancelled, done, parked, awaiting review, rework, the owner's decision, waiting to merge. */
export function todoChipState(o: { toStart: boolean; status: string; done: boolean; stage?: string; pr?: string;
  todo?: { done_at?: string | null; supervision?: Supervision } }): string | undefined {
  if (o.toStart) return t("assistant-tasks.plan_waiting");
  const review = o.todo ? reviewState(o.todo) : "";
  if (review === "user_check") return t("assistant-tasks.review_user_check");
  if (review === "accepted" || o.status === "accepted") return t("assistant-tasks.review_accepted");
  if (review === "cancelled" || o.status === "cancelled") return t("assistant-tasks.review_cancelled");
  if (o.done || o.status === "done") return stateName("done");
  if (review === "parked") return t("assistant-tasks.review_parked");
  if (review === "needs_user") return t("assistant-tasks.review_needs_user");
  // Awaiting review and rework are the to-do's state only while its Agent is not at work or asking: otherwise the row is
  // that Agent's (its status column says so) and a review chip beside it would contradict it.
  const agentBusy = o.status === "working" || o.status === "waiting" || o.status === "needs_you" || o.status === "unread";
  if (review === "pending_review" && !agentBusy) return t("assistant-tasks.review_pending");
  if (review === "rework" && !agentBusy) return t("assistant-tasks.review_rework", { round: o.todo?.supervision?.round ?? 1 });
  if (o.stage === "merge" && o.pr) return t("assistant-tasks.todo_waiting_merge");
  return undefined;
}

/** "To-do: Parked": a to-do state as its chip says it. */
export function todoChipLabel(state: string): string {
  return t("assistant-tasks.todo_chip", { state });
}
