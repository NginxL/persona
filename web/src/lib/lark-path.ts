// A Lark request's path for one paired account (ol_account) and one manager. Not in lark-accounts.ts: the chat list
// (lark-chats.ts, on the first screen) needs only this, and importing it from there put the accounts store and its
// words on every first screen.
import { withManager } from "./manager-choice";

/** `path` (`/lark/…`, with or without a query) naming account `ol`, and `manager` when the chat is another manager's
 * (an inbox item's stamp, docs/assistant/managers.md "Clients": the hub reads `?manager=`); unchanged for the hub's own account
 * (undefined) and the default manager, or for what it names already. */
export function larkPath(path: string, ol?: number, manager?: string): string {
  const named = ol === undefined || /[?&]ol_account=/.test(path) ? path : `${path}${path.includes("?") ? "&" : "?"}ol_account=${ol}`;
  return withManager(named, manager);
}
