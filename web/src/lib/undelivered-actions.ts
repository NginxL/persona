// Retry and Delete on a kept message (lib/undelivered.ts, #10194). Only the kept message's bubble uses them, so they
// come with its chunk (views/chat/UndeliveredBubbles.tsx), not with the first screen (#10357).
import { api, enc, sessionPath } from "./api";
import { getSession, upsertSession } from "./store";
import type { UndeliveredMessage } from "./types";

type Kept = Pick<UndeliveredMessage, "id" | "computer">;

/** Where a kept message's Retry and Delete go: a Persona chat's is kept by its assistant (#10194); one held for an Agent
 *  while its computer was away (`computer` set, #10661) by that computer's manager, under the session. */
function keptPath(mid: string, sid: string, m: Kept): string {
  return m.computer ? `${sessionPath(mid, sid)}/held/${enc(m.id)}` : `/assistant/conversations/${enc(sid)}/undelivered/${enc(m.id)}`;
}

function dropLocally(mid: string, sid: string, id: string) {
  const row = getSession(mid, sid);
  if (!row?.undelivered) return;
  upsertSession({ ...row, undelivered: row.undelivered.filter((m) => m.id !== id) }, { undelivered: true });
}

/** Retry on one that gave up: the manager tries it again from now. */
export async function retryUndelivered(mid: string, sid: string, m: Kept): Promise<void> {
  await api(`${keptPath(mid, sid, m)}/retry`, { method: "POST", json: {} });
  const row = getSession(mid, sid);
  if (row?.undelivered) {
    upsertSession({ ...row, undelivered: row.undelivered.map((x) => (x.id === m.id ? { ...x, state: "waiting" as const, error: undefined } : x)) }, { undelivered: true });
  }
}

/** Delete: it is never sent. */
export async function deleteUndelivered(mid: string, sid: string, m: Kept): Promise<void> {
  await api(keptPath(mid, sid, m), { method: "DELETE" });
  dropLocally(mid, sid, m.id);
}
