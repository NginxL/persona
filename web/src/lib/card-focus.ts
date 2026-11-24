// The card a row in "Overview"'s "Needs you" was opened for (#4089): the chat puts it first, so on a phone it is the one shown in
// full and not folded into "N more" under another card of the same agent. It rides on that history entry only: the
// sidebar, a notification or a new tab open the chat without it.
import type { Route } from "./router";
import { requestHome, requestHomeMachine } from "./child-sessions";
import { isSide, sideFoldOn } from "./side-fold";
import type { Interaction } from "./types";

const KEY = "card";

/** The history state that opens a chat with `id` first. */
export function cardState(id: string): Record<string, unknown> {
  return { [KEY]: id };
}

/** The card the current history entry was opened for, if any. */
export function focusedCard(state: unknown = typeof history !== "undefined" ? history.state : null): string | undefined {
  const id = state && typeof state === "object" ? (state as Record<string, unknown>)[KEY] : undefined;
  return typeof id === "string" ? id : undefined;
}

/** The cards with `id` first, the rest in their order; the same list when it is first or not among them. */
export function focusedFirst<T extends Pick<Interaction, "id">>(cards: T[], id: string | undefined): T[] {
  const i = id === undefined ? -1 : cards.findIndex((c) => c.id === id);
  return i <= 0 ? cards : [cards[i], ...cards.slice(0, i), ...cards.slice(i + 1)];
}

/** A script with no session opens its card, never an empty conversation URL; a child session's card opens its
 *  parent's page, where it is answered (#5120); a lead's worker's opens the worker's link, which is the lead's page with
 *  the worker in its Team panel (#5180, App); a side session's opens the side's link, which is its parent's page with
 *  the side in its Side sessions panel, and a card of a row that belongs to a side the side's link too (#7944, App). */
export function requestRoute(ix: Pick<Interaction, "id" | "machine_id" | "session_id" | "folded_under" | "folded_under_machine_id" | "folded_as" | "folded_side">): Route {
  if (ix.session_id === "") return { name: "request", id: ix.id };
  if (isSide(ix)) return { name: "session", mid: ix.machine_id, sid: ix.session_id };
  if (ix.folded_side && sideFoldOn()) return { name: "session", mid: ix.machine_id, sid: ix.folded_side };
  if (ix.folded_as === "worker") return { name: "session", mid: ix.machine_id, sid: ix.session_id };
  return { name: "session", mid: requestHomeMachine(ix), sid: requestHome(ix) };
}
