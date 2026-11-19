// The owner's messages to a Persona conversation that could not go in (its assistant did not start, its computer was
// away), which the conversation's manager keeps and sends again by itself (#10194, docs/assistant/assistant.md
// "Messages that could not go in"). The row carries them (`undelivered`), so every device shows them, not only the one
// that sent: as the owner's own bubbles marked not delivered, with Delete, and Retry once it gave up. This module holds
// what a send and the chat need on the first screen; Retry and Delete are in lib/undelivered-actions.ts, which only
// the bubble's own chunk loads (views/chat/UndeliveredBubbles.tsx, #10357).
import { ApiError } from "./api";
import { getSession, upsertSession } from "./store";
import { parseUserText, type UserMessage } from "./turns";
import type { Item, Session, UndeliveredMessage } from "./types";

/** A send's answer when the manager kept the message to send later: 202 {"delivered":"later","undelivered":{…}}. */
export interface KeptReply {
  delivered?: string;
  undelivered?: UndeliveredMessage;
  /** A replayed operation the hub has no answer for yet (202 {"state":"unknown"|"pending"}): not a delivery. */
  state?: string;
}

export const isKept = (reply: KeptReply | undefined): boolean => reply?.delivered === "later";

/**
 * A 202 receipt that only says the operation's state (a replay while the first is unsettled, or one the hub never
 * learned the outcome of) is not a delivered message: it becomes the in-doubt failure the bubble shows with Check,
 * never the accepted send that used to wait for the transcript and then vanish (#10194).
 */
export function assertDelivered(reply: KeptReply | undefined): void {
  if (reply && !reply.delivered && (reply.state === "unknown" || reply.state === "pending")) {
    throw new ApiError(202, "delivery unknown", { delivered: "unknown", state: reply.state });
  }
}

/** Shows a kept message on its row at once, before the hub's next list carries it. */
export function showKept(mid: string, sid: string, kept: UndeliveredMessage | undefined): void {
  const row = getSession(mid, sid);
  if (!row || !kept?.id) return;
  const list = row.undelivered ?? [];
  if (list.some((m) => m.id === kept.id)) return;
  upsertSession({ ...row, undelivered: [...list, kept] });
}

/** The kept messages to draw: not one the transcript has already (the row may lag behind the line). */
export function shownUndelivered(row: Pick<Session, "undelivered"> | undefined, items: readonly Pick<Item, "client_msg_id">[], sending: readonly string[] = []): UndeliveredMessage[] {
  const list = row?.undelivered ?? [];
  if (!list.length) return list;
  const landed = new Set([...items.map((i) => i.client_msg_id).filter(Boolean), ...sending]);
  return list.filter((m) => !landed.has(m.id));
}

/** The owner's bubble a kept message draws as: the same while its chunk loads as once it is in (ChatView). */
export const keptBubble = (m: UndeliveredMessage): UserMessage => ({ id: `undelivered:${m.id}`, client_msg_id: m.id, ...parseUserText(m.text), images: [] });
