// Older pages of a Feishu chat, loaded as the list nears its top (iOS LarkHistoryPaging, #1033), and where the list
// stands against its newest end for "Back to latest" (iOS LarkFollow / LarkPager.latest, #1034). Pure logic; LarkThread wires it.
import { comparePosition, type LarkMessage, type LarkPage } from "./inbox";
import type { LarkConversation } from "./lark-chats";

/** Why older paging stopped: it waits for a click on the header line. */
export type HistoryProblem = "failed" | "stopped" | "account_changed" | "signed_out";

export function historyProblem(code: string | undefined): HistoryProblem {
  switch (code) {
    case "lark_account_changed": return "account_changed";
    case "lark_not_logged_in": return "signed_out";
    default: return "failed";
  }
}

/** One older page at a time. A failed or non-progressing page waits for an explicit retry; `reset` (another chat,
 * a replaced window) invalidates a reply still on its way. */
export class OlderPaging {
  private pending: { id: number; cursor: string } | null = null;
  private seq = 0;
  private visited = new Set<string>();
  problem: HistoryProblem | null = null;

  get loading(): boolean {
    return this.pending !== null;
  }

  /** A request for the page before `cursor`, or null: one is on its way, there is none, or it stopped (unless retry). */
  begin(cursor: string | undefined, retry = false): { id: number; cursor: string } | null {
    if (this.pending || !cursor || (this.problem && !retry)) return null;
    this.pending = { id: ++this.seq, cursor };
    this.problem = null;
    return this.pending;
  }

  fail(req: { id: number }, problem: HistoryProblem): boolean {
    if (this.pending?.id !== req.id) return false;
    this.pending = null;
    this.problem = problem;
    return true;
  }

  /** The page to merge in front of `have`, or null for a reply from an earlier window. Rows already shown are
   * dropped; an empty or duplicate page that still claims more, or a cursor seen before, stops paging (keeping what
   * it brought) and leaves the same boundary to retry, so it never loops. */
  finish(req: { id: number; cursor: string }, page: LarkPage, have: LarkMessage[]): LarkPage | null {
    if (this.pending?.id !== req.id) return null;
    this.pending = null;
    this.visited.add(req.cursor);
    const shown = new Set(have.map((m) => m.id));
    const messages = (page.messages ?? []).filter((m) => !shown.has(m.id));
    const next = page.older_cursor ?? messages[0]?.position;
    let older_cursor = page.older_cursor;
    if (page.has_older && (!messages.length || !next || this.visited.has(next))) {
      this.problem = "stopped";
      older_cursor = req.cursor;
    }
    return { ...page, messages, older_cursor };
  }

  reset(): void {
    this.pending = null;
    this.problem = null;
    this.visited.clear();
  }
}

/** The list is within half a screen of its top: time for the next older page. */
export function nearTop(el: { scrollTop: number; clientHeight: number }): boolean {
  return el.scrollTop <= el.clientHeight / 2;
}

/** How far the list is from its newest end, in screens. */
export function screensFromEnd(el: { scrollTop: number; clientHeight: number; scrollHeight: number }): number {
  if (el.clientHeight <= 0) return 0;
  return Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop) / el.clientHeight;
}

/** The list shows its newest message: within a twentieth of a screen of its end (iOS larkFollowsEnd). */
export function atNewest(el: { scrollTop: number; clientHeight: number; scrollHeight: number }): boolean {
  return screensFromEnd(el) < 0.05;
}

/** "Back to latest" shows when the window is not at the newest messages, or the list is more than a screen up from them. */
export function showsJumpToLatest(hasNewer: boolean | undefined, el: { scrollTop: number; clientHeight: number; scrollHeight: number }): boolean {
  return Boolean(hasNewer) || screensFromEnd(el) > 1;
}

/** How many of the newest messages are kept for an instant "Back to latest" (iOS keeps 80). */
export const LATEST_KEPT = 80;

/** The newest messages of a window that reaches the newest end, kept to go back to without waiting. */
export function latestOf(page: LarkPage): LarkPage {
  const messages = page.messages.slice(-LATEST_KEPT);
  const cut = messages.length < page.messages.length;
  return { ...page, messages, has_newer: false, newer_cursor: undefined, has_older: page.has_older || cut,
    older_cursor: cut ? messages[0]?.position : page.older_cursor };
}

/** The position after `p`: Feishu positions count up by one per message. */
export function nextPosition(p: string): string {
  if (!/^\d+$/.test(p)) return p;
  return (BigInt(p) + 1n).toString();
}

/** Where a chat opened from "Messages" lands and what it can jump back to. */
export interface LarkEntry {
  /** The first unread message: the one after Feishu's read position. */
  unreadFrom?: string;
  /** An unread @me past the read position. */
  mention?: string;
}

/** Where a chat opened from its row in "Messages" lands, as Feishu, iOS (LarkChatView.load) and Android
 * (larkListTarget) open it: an unread chat at its first unread message, with its unread @me kept for a jump; a read
 * chat, a row not loaded yet or a topic group (its read state is per topic) at the newest. */
export function entryOf(row: Pick<LarkConversation, "unread" | "read_position" | "mention_position" | "is_topic"> | undefined): LarkEntry {
  const read = row?.read_position;
  if (!row || row.is_topic || !(row.unread > 0) || !read || !/^\d+$/.test(read)) return {};
  const mention = row.mention_position && comparePosition(row.mention_position, read) > 0 ? row.mention_position : undefined;
  return { unreadFrom: nextPosition(read), ...(mention ? { mention } : {}) };
}

/** The first message at or after `position` in a window, or undefined when the window does not reach it. */
export function firstAtOrAfter(messages: LarkMessage[], position: string | undefined): LarkMessage | undefined {
  return position ? messages.find((m) => comparePosition(m.position, position) >= 0) : undefined;
}

/** The message the New messages line goes above: the first at or after `position`, when the window shows what came
 * before it (a window that starts later, as after "Back to latest", has no line). */
export function newMessagesLineAt(page: Pick<LarkPage, "messages" | "has_older">, position: string | undefined): string | undefined {
  const m = firstAtOrAfter(page.messages, position);
  if (!m) return undefined;
  return m !== page.messages[0] || !page.has_older ? m.id : undefined;
}

/** A jump pill above the list ("First unread", "Mentioned") shows while its message is not on screen and lies above
 * it: before a window that has older pages, or loaded but scrolled past. `rowTop` is that message's top against the
 * list's viewport, undefined when it is not drawn. */
export function showsJumpUp(target: string | undefined, page: Pick<LarkPage, "messages" | "has_older">, rowTop: number | undefined): boolean {
  const first = page.messages[0];
  if (!target || !first) return false;
  if (comparePosition(target, first.position) < 0) return Boolean(page.has_older);
  return rowTop !== undefined && rowTop < 0;
}
