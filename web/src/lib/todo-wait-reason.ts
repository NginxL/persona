// What a waiting to-do waits on (#11090; docs/assistant/todos.md "State calculation rules", row 4.5). A to-do is Waiting
// when one of its Agents ended its turn and something brings it back (todos.ts agentWaits, #3274). One word for all of
// them read as "it waits for me", so its label names the wait instead: "Waiting on you" in the attention color only when
// it waits on its owner by the sidebar badge's own predicate (todo-triage.ts waitsOnOwner, #11085), so the label and the
// badge agree; otherwise a neutral word for what it waits on, or "Paused (nothing needed from you)" when nothing says.
// Pure: it reads only the to-do, its Agents' sessions and its placement, never what an Agent says in words.

import { stateOf } from "./format";
import { waitName, type WaitReason } from "./state-labels";
import { openMergePRs, triage, waitsOnOwner, type Triage } from "./todo-triage";
import { agentWaits, heldDelivery, shownStatus, TODO_STATUS_INFO, todoStatusLabel, type SessionLookup, type Todo, type TodoStatus, type TodoTone } from "./todos";

export type { WaitReason };

/** What its first waiting Agent (agentWaits, in the to-do's order as waitingLine reads it) waits on, by its work state
 *  (docs/assistant/agent-work.md "Model"): its own background commands, subagents, Monitors or workflows, or an
 *  external wait that names nothing and has no wake (lib/work.ts hubWaits: it wakes itself); the hub's retry after a
 *  moment's model failure (`transient`); another Agent (`agent`, or a target Agent named); a named outside thing
 *  (`external` with its `wait`). An external wait the hub checks back on without naming it says nothing (undefined),
 *  and so does a to-do none of whose Agents waits. */
function agentWait(todo: Todo, lookup: SessionLookup | undefined): WaitReason | undefined {
  for (const a of todo.agents ?? []) {
    if (a.released_at || a.start_error) continue;
    const s = lookup?.(a.machine_id, a.session_id);
    if (!s || !agentWaits(s)) continue;
    if (stateOf(s) === "working") return "background";
    const w = s.work!;
    if (w.on === "transient") return "retry";
    if (w.on === "agent" || w.agent) return "prerequisite";
    if (w.wait?.trim()) return "external";
    return w.wake_at ? undefined : "background";
  }
  return undefined;
}

/** Its finished work is held short of live by someone else (heldDelivery: at a PR, merged, off; `unblock` agent or
 *  other), a fresh PR of it is open (openMergePRs, the Parked section's merge wait), its supervisor parked it on a
 *  merge review, or its latest check found it undelivered. */
function waitsToShip(todo: Todo, now: number): boolean {
  if (heldDelivery(todo) || openMergePRs(todo, now).length) return true;
  const sup = todo.supervision;
  if (sup?.state === "parked" && sup.parked?.evidence?.kind === "merge_review") return true;
  return checkVerdict(todo) === "undelivered";
}

/** What a park waits on (`waiting_on`) when it waits outside Persona: someone else, an outside event, a date. */
const PARKED_OUTSIDE: ReadonlySet<string> = new Set(["someone_else", "external_event", "date"]);

/** Its supervisor parked it (#3804) on someone else, an outside event or a date: outside Persona. A park's evidence
 *  (#9289, the kinds task-wait-info.ts taskWait names) has the kind its `waiting_on` says (park_evidence.go refuses
 *  any other), and a merge review is waitsToShip's. A legacy `owner_priority` park is not the owner's here: the hub
 *  turns it into a decision (needs_user), which waitsOnOwner reads. */
function parkedWait(todo: Todo): WaitReason | undefined {
  const sup = todo.supervision;
  return sup?.state === "parked" && sup.parked && PARKED_OUTSIDE.has(sup.parked.waiting_on) ? "external" : undefined;
}

/** Its completion check's verdict while it still stands: no reopen after it (stoppedReason reads it the same way). */
function checkVerdict(todo: Todo): string | undefined {
  const c = todo.check;
  if (!c?.verdict || todo.done_at) return undefined;
  if (todo.reopened_at && !(Date.parse(c.at) >= Date.parse(todo.reopened_at))) return undefined;
  return c.verdict;
}

/**
 * What a to-do whose status is Waiting waits on, first that holds:
 *
 *  1. owner         it waits on its owner (waitsOnOwner: a decision only they can make, or work held for their OK)
 *  2. background    its Agent's own background work runs, and it wakes itself
 *     retry         the hub retries after a moment's model failure
 *     prerequisite  its Agent waits on another Agent
 *  3. prerequisite  … or on something the to-do's facts name: its prerequisite to-dos (`prerequisite_blockers`)
 *     delivery      a merge or deploy (waitsToShip)
 *     external      something outside Persona its Agent named (`work.wait`)
 *  4. external      its supervisor's park on someone else, an event or a date (parkedWait), or its check's `waiting`
 *  5. unknown       nothing says: "Paused (nothing needed from you)", never the attention color
 *
 * A check's `needs_user` verdict or an Agent's words never make it the owner's: only waitsOnOwner does.
 *
 * @param todo   the to-do (todoStatus says waiting)
 * @param lookup its Agents' sessions, as todoStatus read them
 * @param placed its placement when the caller has it (the To-dos page's triage); else worked out here, which for a
 *               waiting to-do comes out the same whatever the page restored or superseded
 */
export function waitReason(todo: Todo, lookup: SessionLookup | undefined, placed?: Triage, now = Date.now()): WaitReason {
  if (waitsOnOwner(todo, placed ?? triage(todo, lookup, now))) return "owner";
  const agent = agentWait(todo, lookup);
  if (agent && agent !== "external") return agent;
  if (todo.prerequisite_blockers?.length) return "prerequisite";
  if (waitsToShip(todo, now)) return "delivery";
  if (agent) return agent;
  return parkedWait(todo) ?? (checkVerdict(todo) === "waiting" ? "external" : "unknown");
}

/** waitReason for a waiting to-do, undefined for any other status. */
export function waitOf(todo: Todo, status: TodoStatus, lookup: SessionLookup | undefined, placed?: Triage): WaitReason | undefined {
  return status === "waiting" ? waitReason(todo, lookup, placed) : undefined;
}

/** The tone of a wait's word: attention only for one on the owner, the neutral idle tone for the rest. */
export function waitTone(r: WaitReason): TodoTone {
  return r === "owner" ? "attention" : "idle";
}

/** A to-do's state word and tone (TODO_TONE_CLASS) as the lists draw them: a waiting one's says what it waits on
 *  (`wait`, from waitOf; none reads as unknown), every other status's are todoStatusLabel's and the tone of `toneOf`'s
 *  TODO_STATUS_INFO, as each list took it before: shownStatus (the To-dos page), or the status itself (the overview). */
export function todoLook(todo: Todo, status: TodoStatus, wait?: WaitReason, toneOf: TodoStatus = shownStatus(todo, status)): { label: string; tone: TodoTone } {
  if (status === "waiting") return { label: waitName(wait ?? "unknown"), tone: waitTone(wait ?? "unknown") };
  return { label: todoStatusLabel(todo, status), tone: TODO_STATUS_INFO[toneOf].tone };
}

/** A status's word as TODO_STATUS_INFO says it, a waiting one's naming its wait. */
export function statusWord(status: TodoStatus, wait?: WaitReason): string {
  return status === "waiting" ? waitName(wait ?? "unknown") : TODO_STATUS_INFO[status].label;
}
