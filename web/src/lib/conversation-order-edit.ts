// Edit-only ordering operations (#12017). Keep these out of the module the startup sidebar reads for display.
import type { Session } from "./types";
import { byConversationOrder } from "./conversation-order";

/** The complete id order after moving `from` onto `to`'s place. Ids saved by a wider client stay at the end. */
export function conversationOrderAfterMove<T extends Pick<Session, "session_id">>(rows: readonly T[], saved: readonly string[] | undefined, from: string, to: string): string[] {
  const ids = byConversationOrder(rows, saved).map((row) => row.session_id);
  const source = ids.indexOf(from), target = ids.indexOf(to);
  if (source >= 0 && target >= 0 && source !== target) ids.splice(target, 0, ...ids.splice(source, 1));
  return [...ids, ...(saved ?? []).filter((id) => !ids.includes(id))];
}

/** One accessible step, using the same full-list rule as a drag. */
export function conversationOrderAfterStep<T extends Pick<Session, "session_id">>(rows: readonly T[], saved: readonly string[] | undefined, id: string, by: -1 | 1): string[] {
  const ids = byConversationOrder(rows, saved).map((row) => row.session_id);
  const at = ids.indexOf(id), to = at + by;
  if (at < 0 || to < 0 || to >= ids.length) return [...ids, ...(saved ?? []).filter((savedId) => !ids.includes(savedId))];
  return conversationOrderAfterMove(rows, saved, id, ids[to]);
}
