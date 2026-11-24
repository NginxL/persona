import { decisionDismissed, reviewState } from "./supervision";
import { decisionLaterKey, type NeedsYouItem } from "./assistant-entries";
import { heldDelivery, openAsk } from "./todos";
import { hubNeedsYou } from "./todo-triage";
import type { Interaction } from "./types";

/** Current cards only its user can answer. Agent plan/permission approvals and terminal dialogs are not owner questions. */
export function directUserInteraction(ix: Interaction): boolean {
  if (ix.delegating) return false;
  if (ix.kind === "question" || ix.kind === "elicitation") return true;
  return ix.kind === "permission" && ix.owner_proposal === true;
}

/** A current owner decision or an explicit acceptance, not a generic retry, acknowledgement or supervisor review. */
function ownerDecision(item: Extract<NeedsYouItem, { kind: "task" }>, deferred: Record<string, unknown>): boolean {
  const todo = item.todo;
  if (todo.done_at || item.status === "failed") return false;
  // Delivery acceptance is its own explicit owner gate, not the dismissed review round that preceded it.
  if (heldDelivery(todo)?.unblock === "user") return true;
  if (item.status === "not_started" || decisionDismissed(todo) || deferred[decisionLaterKey(todo)]) return false;
  const ask = openAsk(todo);
  if (ask && (ask.questions?.length || ask.outcome === "decide" || !ask.outcome || ask.why_owner)) return true;
  const review = reviewState(todo);
  return review === "needs_user" || review === "user_check";
}

/** The one projection behind both the Needs-you number and the rows that number opens. */
export function directUserWaiting(items: NeedsYouItem[], deferred: Record<string, unknown> = {}): NeedsYouItem[] {
  const out: NeedsYouItem[] = [];
  for (const item of items) {
    // A plan waiting for Start is the owner's decision (one is carded only when it needs the owner); a message draft
    // still needs its owner's explicit Send authorization.
    if (item.kind === "plan") { out.push(item); continue; }
    if (item.kind === "message") { out.push(item); continue; }
    // a to-do the hub sent a view of is the owner's exactly when the hub counts it in needs_you (assistant-entries
    // already left out a step its parent carries): the tray, the To-dos badge and the hub say one number
    const hub = item.kind === "task" ? hubNeedsYou(item.todo, () => false) : undefined;
    if (item.kind === "task" && hub !== undefined) {
      if (hub) out.push({ ...item, asks: item.asks.filter(directUserInteraction) });
      continue;
    }
    if (item.kind === "task" && (item.status === "failed" || (item.status === "not_started" && heldDelivery(item.todo)?.unblock !== "user"))) continue;
    const asks = item.asks.filter(directUserInteraction);
    if (asks.length) out.push({ ...item, asks });
    else if (item.kind === "task" && ownerDecision(item, deferred)) out.push({ ...item, asks: [] });
  }
  return out;
}

/** The same row-count rule in the main chat and its sidebar rows. Other Agent and To-dos counts do not use this. */
export function directWaitingCount(items: NeedsYouItem[], ownAsks: Interaction[]): number {
  return ownAsks.filter(directUserInteraction).length + items.length;
}

let requested = "";

/** A sidebar click survives navigation: the main chat consumes this when its existing action bar mounts. */
export function requestDirectWaiting(conversation: string): void {
  requested = conversation;
  window.dispatchEvent(new CustomEvent("persona:task-pending", { detail: conversation }));
}

export function takeDirectWaitingRequest(conversation: string): boolean {
  if (requested !== conversation) return false;
  requested = "";
  return true;
}
