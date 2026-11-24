// Shared resume selection for domain spaces: the latest conversation, else the new conversation page.
// Health opens its home directly (#13273); other spaces still use this helper.
import type { Session } from "../../lib/types";
import { newestFirst } from "../assistant/conversations";

export type SpaceEntry = { kind: "wait" } | { kind: "open"; sid: string } | { kind: "new" };

/** `list`: the space's conversations as read (null: not read yet); `error`: why they could not be read. The newest open
 *  one by last activity (`updated_at`, the order the list shows); none, or a list that could not be read, the new
 *  conversation page, which says why the list is missing. */
export function spaceEntry(list: readonly Session[] | null, error = ""): SpaceEntry {
  if (list === null) return error ? { kind: "new" } : { kind: "wait" };
  const latest = newestFirst(list.filter((s) => !s.archived))[0];
  return latest ? { kind: "open", sid: latest.session_id } : { kind: "new" };
}
