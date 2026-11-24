import { backgroundWork, taskBackgroundWork, type BackgroundWork } from "./background-work";
import { cardDismissed, cardFiled, cardSent, startFailure, type FeedItem, type TaskPlan } from "./feed";
import { cardBucket, taskCardSlots, type TaskCardMetadata } from "./task-cards";
import { decisionDismissed, linkedParkInteraction, parkInteractionID, reviewState, withoutParkInteraction } from "./supervision";
import { finishedAt, hubNeedsYou, JUST_DONE_MS, openByView, supersededFailedStarts, triage } from "./todo-triage";
import { awaitsReview, dockAgents, heldDelivery, heldForSupervisor, makeSessionLookup, openAsk, todoRank, todoStatus, withInteractionLookup, type SessionLookup, type Todo, type TodoStatus } from "./todos";
import { byTime } from "./times";
import { homeKey } from "./folded-rows";
import type { Interaction, Session } from "./types";

/**
 * The Assistant's entries above the composer (docs/assistant/unified-entry.md "Needs you and In progress"):
 * Needs you lists the user's turn, In progress the active work, and Parked the external waits, including ready PRs.
 * Each thing has one home and one count. Finished things are not open work: the handed-out ones that finished in the
 * last 24 hours are under Finished (`finished`, #10922), everything else on the board. A
 * task waiting for its supervisor's review is in neither: the web lists it under Persona reviewing (`review`, #8678),
 * however long it has waited. A task is one row whatever its agents: they are not listed on their own. In the main
 * conversation an Agent that belongs to no task (opened by hand) is a row of its own.
 */
export type NeedsYouItem =
  /** A task whose agent asks something (`asks`, oldest first), or that waits for the user's decision.
   *  `countsOpen` says whether the To-dos page's "Not done" counts the to-do (#3331): the row stays visible whatever
   *  it says, the chip badge counts it only when it does. */
  | { kind: "task"; key: string; todo: Todo; status: TodoStatus; asks: Interaction[]; countsOpen: boolean }
  /** An Agent that belongs to no task, asking something. */
  | { kind: "agent"; key: string; session: Session; asks: Interaction[] }
  /** A plan waiting for Start. */
  | { kind: "plan"; key: string; plan: TaskPlan }
  /** A message card waiting for Send. */
  | { kind: "message"; key: string; item: FeedItem };

export type RunningItem =
  /** A dispatched task not finished and not waiting on the user. `countsOpen`: see NeedsYouItem. */
  | { kind: "task"; key: string; card: TaskCardMetadata; todo: Todo; status: TodoStatus; activity?: BackgroundWork; countsOpen: boolean }
  /** A working Agent that belongs to no task. */
  | { kind: "agent"; key: string; session: Session; activity?: BackgroundWork };

/** A task the conversation handed out that finished in the last 24 hours (Finished, #10922). `fresh`: the owner has not
 *  opened it since it finished (the To-dos page's Just finished), so it is drawn as new; seeing it changes only that.
 *  `at`: when it finished (ms, `finishedAt`). */
export interface FinishedItem { kind: "task"; key: string; card: TaskCardMetadata; todo: Todo; status: TodoStatus; fresh: boolean; at: number }

export interface AssistantEntries {
  needsYou: NeedsYouItem[];
  running: RunningItem[];
  /** Open tasks with no progress for 48 hours and nothing running (the To-dos page's shelved, #3331): folded under
   *  In progress, not counted. */
  shelved: RunningItem[];
  /** Explicit holds and fresh ready PR waits, outside In progress; still open tasks. */
  parked: RunningItem[];
  /** Tasks waiting for their supervisor's review (`pending_review`) with nothing of them running or asking, however long
   *  the review has waited (web only, #8678): Persona's to do, not the owner's, so outside Needs you; still open tasks,
   *  counted like the other entries. An asking agent, an open `ask` or `needs_user` makes such a task Needs you instead. */
  review: RunningItem[];
  /** Handed-out tasks that finished in the last 24 hours (JUST_DONE_MS, the To-dos page's Just finished window), seen or
   *  not, newest finished first: the Finished entry (#10922). How a task ended is told here, not by a card in the chat
   *  (#8237). Not open work: its count is its rows, and opening one marks it seen without taking it out. Until #10922
   *  only the unseen ones were listed, as a group under In progress that went as soon as one was opened. */
  finished: FinishedItem[];
}

export interface AssistantEntriesInput {
  conversation: string;
  /** The main conversation's id: it watches every task and the Agents opened by hand. */
  main?: string;
  manager?: string;
  todos: Todo[];
  /** Every session, as the lists show them (answered cards applied). */
  sessions: Session[];
  /** Pending cards. */
  interactions: Interaction[];
  plans: Record<string, TaskPlan>;
  /** The conversation's feed. */
  feed: FeedItem[];
  /** The hub's task card slots for the conversation (`task_cards`). */
  cards?: TaskCardMetadata[];
  /** Message cards and task-decision rounds the user put off locally (`later`). */
  deferred?: Record<string, unknown>;
  /** Looks up a task's agents; defaults to `sessions`. */
  lookup?: SessionLookup;
  now?: number;
  /** Tasks the user brought back from shelved, by id: when (ms). */
  restored?: ReadonlyMap<string, number>;
}

/** Task states that wait on the user: an agent asks, or its turn broke off on an error (a failed start is below). */
const WAITS_ON_YOU: ReadonlySet<TodoStatus> = new Set(["needs_you", "interrupted"]);

const sessionKey = (mid: string, sid: string) => `${mid}/${sid}`;

/** A task's status when some of its Agents ask under another task (`own`: its status from its own Agents alone): it
 *  does not wait on the user for them, so a Needs you that only those bring is the task at work instead. */
function withNeeds(all: TodoStatus, own: TodoStatus): TodoStatus {
  return all === "needs_you" && own !== "needs_you" ? (own === "not_started" ? "working" : own) : all;
}

/** A task's status, failed too when only its plan says the start failed (a hub from before #1077). */
export function entryStatus(todo: Todo, lookup: SessionLookup, plans: Record<string, TaskPlan>): TodoStatus {
  const st = todoStatus(todo, lookup);
  return st === "not_started" && startFailure(todo, plans) ? "failed" : st;
}

/** The task waits for the user's decision: an open task's ask, or its supervisor's `needs_user` on an open task. A
 *  finished task never does, whatever ask it still carries: the hub's To-dos triage files it with the finished ones
 *  and the tray has nothing to answer, so it would keep the badge up for good (#12974). A `pending_review` is its
 *  supervisor's to do, however long it has waited (#8678). */
export function taskAwaitsDecision(todo: Todo): boolean {
  if (todo.done_at) return false;
  if (openAsk(todo)) return true;
  return todo.supervision?.state === "needs_user" || todo.supervision?.state === "user_check";
}

/** The task waits for its supervisor's review and nothing of it runs: an open `pending_review` whose status is a
 *  finished turn (`awaitsReview`), or an owner hold its Agent reported that its supervisor has not read yet
 *  (`heldForSupervisor`). An agent at work again keeps it In progress. */
function underReview(todo: Todo, status: TodoStatus): boolean {
  if (heldForSupervisor(todo) && (status === "follow_up" || status === "unread" || status === "not_started")) return true; // #13191
  return !todo.done_at && reviewState(todo) === "pending_review" && awaitsReview(todo, status);
}

/** The message card still waits for Send: not sent, filed, dismissed, or put off. */
export function messageWaits(it: FeedItem, deferred: Record<string, unknown> = {}): boolean {
  return it.kind === "message" && !cardSent(it) && !cardFiled(it) && !cardDismissed(it) && !deferred[it.id];
}

/** The review round, not the task id alone: a later decision must remain visible. */
export function decisionRound(todo: Todo): string {
  return todo.supervision?.reviews?.at(-1)?.at || todo.decision_review?.review_at || todo.ask?.asked_at || "";
}

export function decisionLaterKey(todo: Todo): string {
  return `decision:${todo.manager || ""}:${todo.id}:${decisionRound(todo)}`;
}

export function assistantEntries(input: AssistantEntriesInput): AssistantEntries {
  const { conversation, main, todos, sessions, interactions, plans, feed } = input;
  const now = input.now ?? Date.now();
  const lookup = withInteractionLookup(input.lookup ?? makeSessionLookup(sessions), interactions);
  const isMain = !!main && main === conversation;
  const byTodo = new Map(todos.map((todo) => [todo.id, todo]));
  // failed starts whose work finished in a related to-do: closed, as the To-dos page has them (#3713)
  const superseded = supersededFailedStarts(todos);
  const asksOf = new Map<string, Interaction[]>();
  for (const ix of [...interactions].sort((a, b) => byTime(a.created_at, b.created_at) || a.id.localeCompare(b.id))) {
    // a child session's card is asked under its parent (#5120): the parent's task or Agent row holds it
    const k = homeKey(ix);
    asksOf.set(k, [...(asksOf.get(k) ?? []), ix]);
  }

  const asking: NeedsYouItem[] = [];
  const deciding: { item: NeedsYouItem; rank: number; at: string }[] = [];
  const open: { item: RunningItem; rank: number; at: string }[] = [];
  const shelved: { item: RunningItem; at: string }[] = [];
  const parked: { item: RunningItem; at: string }[] = [];
  const review: { item: RunningItem; rank: number; at: string }[] = [];
  const finished: FinishedItem[] = [];
  const folded = new Set<string>();
  const slots = taskCardSlots(conversation, input.cards ?? [], feed, plans, todos, isMain);
  const openOf = openByView(todos);
  const activeAgents = (todo: Todo) => (todo.done_at ? [] : (todo.agents ?? []).filter((a) => !a.released_at));
  const homeAgents = (todo: Todo) => (todo.done_at ? [] : (todo.agents ?? []).filter((a) => !a.released_at || parkInteractionID(todo)));
  // One home per request (docs/design/DESIGN_SYSTEM.md 4.8): an Agent several of these tasks list asks under one of them,
  // the task the hub ties its session to (`todo`), else the first that lists it. The others neither repeat its cards nor
  // wait on the user for them; they are in progress.
  const listing = new Map<string, string[]>();
  for (const card of slots) {
    const todo = card.todo_id ? byTodo.get(card.todo_id) : undefined;
    if (!todo) continue;
    for (const a of homeAgents(todo)) {
      const k = sessionKey(a.machine_id, a.session_id);
      const ids = listing.get(k) ?? [];
      if (!ids.includes(todo.id)) listing.set(k, [...ids, todo.id]);
    }
  }
  const homeOf = (mid: string, sid: string): string | undefined => {
    const ids = listing.get(sessionKey(mid, sid));
    const tied = lookup(mid, sid)?.todo?.id;
    return tied && ids?.includes(tied) ? tied : ids?.[0];
  };
  for (const card of slots) {
    const sourceTodo = card.todo_id ? byTodo.get(card.todo_id) : undefined;
    // a plan task with no to-do yet is its plan's row (below); a to-do the hub no longer sends is finished
    if (!sourceTodo) continue;
    const candidate = linkedParkInteraction(sourceTodo, interactions);
    const linkedHome = candidate ? homeOf(candidate.machine_id, candidate.session_id) : undefined;
    const todo = linkedHome !== undefined && linkedHome !== sourceTodo.id ? withoutParkInteraction(sourceTodo) : sourceTodo;
    const active = activeAgents(todo);
    const own = active.filter((a) => homeOf(a.machine_id, a.session_id) === todo.id);
    // what the task waits on the user for is its own Agents' (an Agent another task holds asks there)
    const status = own.length < active.length ? withNeeds(entryStatus(todo, lookup, plans), todoStatus({ ...todo, agents: own }, lookup)) : entryStatus(todo, lookup, plans);
    const linked = linkedParkInteraction(todo, interactions);
    // a card the task's supervising conversation holds (delegating) is that conversation's until it passes it up
    const routed = own.flatMap((a) => asksOf.get(sessionKey(a.machine_id, a.session_id)) ?? []);
    const ownAsks = routed.filter((ix) => !ix.delegating);
    const asks = linked && !ownAsks.some((ix) => ix.id === linked.id) ?
      [...ownAsks, linked].sort((a, b) => byTime(a.created_at, b.created_at) || a.id.localeCompare(b.id)) : ownAsks;
    const key = `task:${todo.id}`;
    const bucket = cardBucket(card, todo, plans, lookup, now, input.restored?.get(todo.id) ?? 0, superseded.has(todo.id));
    const countsOpen = bucket === "open";
    // a failed start carries done_at (the hub ends it), yet it is the user's to retry while its bucket says open; a
    // supervised task's failed start is its supervisor's to retry or remedy, and an Agent whose every card its
    // supervisor holds waits on that conversation, not on the user
    const supervised = !!todo.supervision?.supervisor;
    const held = supervised && routed.length > 0 && ownAsks.length === 0;
    const waits = status === "failed" ? bucket === "open" && !supervised : !todo.done_at && WAITS_ON_YOU.has(status) && !held;
    const decision = taskAwaitsDecision(todo);
    const deliveryApproval = heldDelivery(todo)?.unblock === "user";
    // the hub's view decides when there is one, so this tray, the To-dos badge and the hub's needs_you agree
    const hub = hubNeedsYou(todo, openOf);
    if (asks.length) asking.push({ kind: "task", key, todo, status, asks, countsOpen });
    else if (hub ?? (waits || decision || deliveryApproval)) {
      // Only the review decision is hidden; a live Agent question above still has its home.
      if (deliveryApproval || !(status !== "needs_you" && decision && (decisionDismissed(todo) || input.deferred?.[decisionLaterKey(todo)]))) {
        deciding.push({ item: { kind: "task", key, todo, status, asks, countsOpen }, rank: todoRank(todo, status), at: card.at });
      }
    }
    // its supervisor's review (#8678): Persona's own work, not the owner's and not a wait on the upstream maintainer, so
    // its own group whatever the To-dos page files it under, even with a fresh open PR; once reviewed (accepted, or parked
    // on the PR) the PR rule below files it
    else if (underReview(todo, status)) review.push({ item: { kind: "task", key, card, todo, status, countsOpen }, rank: todoRank(todo, status), at: card.at });
    // Use the To-dos page's wait classification. Live work and owner decisions outrank a hold or an open PR.
    else if (reviewState(todo) !== "pending_review" && triage(todo, lookup, now, input.restored?.get(todo.id) ?? 0, superseded.has(todo.id)).section === "parked") parked.push({ item: { kind: "task", key, card, todo, status, countsOpen }, at: card.at });
    else if (bucket === "open" || reviewState(todo) === "pending_review") open.push({ item: { kind: "task", key, card, todo, status, activity: taskBackgroundWork(todo, lookup), countsOpen }, rank: todoRank(todo, status), at: card.at });
    else if (bucket === "shelved") shelved.push({ item: { kind: "task", key, card, todo, status, countsOpen: false }, at: card.at });
    else {
      // finished work a conversation handed out, for a day after it finished, seen or not (#10922); a failed start a
      // related to-do finished is history, not news
      const tr = triage(todo, lookup, now, input.restored?.get(todo.id) ?? 0, superseded.has(todo.id));
      const ended = finishedAt(todo, tr);
      const seenAt = Date.parse(todo.seen_at ?? "");
      if ((tr.section === "just_done" || tr.section === "done") && status !== "failed" && todo.origin && ended > 0 && now - ended < JUST_DONE_MS) {
        // Closed and accepted work is already Done on the To-dos page (#5732); Finished has its own unseen marker.
        finished.push({ kind: "task", key, card, todo, status, fresh: Number.isNaN(seenAt) || seenAt < ended, at: ended });
      }
      continue;
    }
    for (const agent of active) folded.add(sessionKey(agent.machine_id, agent.session_id));
    if (linked) folded.add(sessionKey(linked.machine_id, linked.session_id));
  }

  // Agents that belong to no task, the main conversation's only (dockAgents): asking ones wait on the user, working ones run.
  const agentsAsking: NeedsYouItem[] = [];
  const agentsRunning: RunningItem[] = [];
  for (const { session, state } of dockAgents(sessions.map((s) => lookup(s.machine_id, s.session_id) ?? s), todos, conversation, main)) {
    if (folded.has(sessionKey(session.machine_id, session.session_id))) continue;
    const key = `agent:${sessionKey(session.machine_id, session.session_id)}`;
    if (state === "needs_you") agentsAsking.push({ kind: "agent", key, session, asks: asksOf.get(sessionKey(session.machine_id, session.session_id)) ?? [] });
    else agentsRunning.push({ kind: "agent", key, session, activity: backgroundWork(session) });
  }

  const plansWaiting: NeedsYouItem[] = Object.values(plans)
    .filter((plan) => ((isMain && plan.space !== "health") || plan.conversation === conversation) && plan.state === "open" && (!input.manager || !plan.manager || plan.manager === input.manager))
    .sort((a, b) => byTime(a.created_at, b.created_at) || a.id.localeCompare(b.id))
    .map((plan) => ({ kind: "plan", key: `plan:${plan.id}`, plan }));
  const messages: NeedsYouItem[] = feed.filter((it) => messageWaits(it, input.deferred)).map((item) => ({ kind: "message", key: `message:${item.id}`, item }));

  const newest = (a: { at: string; rank?: number }, b: { at: string; rank?: number }) => (a.rank ?? 0) - (b.rank ?? 0) || byTime(b.at, a.at);
  const firstAsk = (item: NeedsYouItem) => (item.kind === "task" || item.kind === "agent" ? item.asks[0]?.created_at : undefined);
  return {
    needsYou: [
      ...[...asking, ...agentsAsking].sort((a, b) => byTime(firstAsk(a), firstAsk(b))),
      ...plansWaiting,
      ...deciding.sort(newest).map((row) => row.item),
      ...messages,
    ],
    running: [...open.sort(newest).map((row) => row.item), ...agentsRunning],
    shelved: shelved.sort(newest).map((row) => row.item),
    parked: parked.sort(newest).map((row) => row.item),
    review: review.sort(newest).map((row) => row.item),
    // newest finished first; the same moment by id, so every client lists them in one order
    finished: finished.sort((a, b) => b.at - a.at || (a.todo.id < b.todo.id ? -1 : a.todo.id > b.todo.id ? 1 : 0)),
  };
}

/** Needs you's rows by the conversation supervising them (#4314, docs/assistant/assistant.md "Background rounds"): a
 *  task's `origin`, else `conversation` (agents, plans, message cards, hand-made to-dos). This conversation's group comes
 *  first, then the others in the order their first row appears; the rows and their order within a group are as given,
 *  so the entry's count is unchanged. One group when every row is this conversation's. */
export function needsYouBySupervisor(items: readonly NeedsYouItem[], conversation: string): { supervisor: string; items: NeedsYouItem[] }[] {
  const groups = new Map<string, NeedsYouItem[]>([[conversation, []]]);
  for (const item of items) {
    const supervisor = item.kind === "task" && item.todo.origin ? item.todo.origin :
      item.kind === "plan" ? item.plan.conversation || conversation : conversation;
    const group = groups.get(supervisor);
    if (group) group.push(item);
    else groups.set(supervisor, [item]);
  }
  return [...groups].map(([supervisor, rows]) => ({ supervisor, items: rows }));
}
