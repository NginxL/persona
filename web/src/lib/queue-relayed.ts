// A Persona chat's queued line from another conversation, read apart for the queue panel (QueuePanel.tsx). Kept out of
// lib/queue.ts, which the first screen loads (ChatView), so only the panel's lazy chunk carries it.

/** A message another conversation sent with conversation_send, as the hub words it in a Persona chat's queue
 *  (`queuedRelays` in server/internal/assistant/conversations.go): 「来自「<title>」：<words>」, in every language. Its
 *  sender's title and its words, so a row can name the sender once instead of wrapping its words in the hub's prefix;
 *  null for the other relayed lines (a task's notice, supervision), which are their own words. */
export function relayedFrom(raw: string): { from: string; text: string } | null {
  const m = /^来自「([^\n]+?)」：/.exec(raw);
  const from = m?.[1].trim();
  return m && from ? { from, text: raw.slice(m[0].length) } : null;
}
