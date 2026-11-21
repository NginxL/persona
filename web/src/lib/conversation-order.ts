// User-chosen Assistant conversation order (#12017). Empty means the existing activity-time rule. Ordering is only a
// presentation preference: the main conversation and Directions keep their own fixed groups, and unknown rows stay visible.
import type { Session } from "./types";

/** Saved ids first in their saved order, then rows the current client did not know in their existing relative order. */
export function byConversationOrder<T extends Pick<Session, "session_id">>(rows: readonly T[], order?: readonly string[]): T[] {
  if (!order?.length) return rows.slice();
  const rank = new Map(order.map((id, i) => [id, i]));
  // Array.prototype.sort is stable: unknown/new rows keep their existing relative order.
  return rows.slice().sort((a, b) => (rank.get(a.session_id) ?? order.length) - (rank.get(b.session_id) ?? order.length));
}
