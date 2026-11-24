import { requestRoute } from "../lib/card-focus";
import { requestHome, requestHomeMachine } from "../lib/child-sessions";
import { stateOf } from "../lib/format";
import { orderPending } from "../lib/pending-order";
import { withWorkView, type Route } from "../lib/router";
import type { AnsweredCard } from "../lib/store";
import { supervisorHeldOnly, userCards } from "../lib/supervisor-held";
import type { Interaction, Session } from "../lib/types";
import { answeredSessions } from "./assistant/answered-lookup";

export interface SidebarPendingRow {
  key: string;
  machineId: string;
  session?: Session;
  cards: Interaction[];
  to: Route;
}

const sessionKey = (mid: string, sid: string) => `session:${JSON.stringify([mid, sid])}`;

/** The current assistant's pending cards and sessions that still need attention without a card. */
export function sidebarPending(sessions: Session[], interactions: Interaction[], answered: Record<string, AnsweredCard>): SidebarPendingRow[] {
  const bySession = new Map(sessions.map((s) => [sessionKey(s.machine_id, s.session_id), s]));
  const rows = new Map<string, SidebarPendingRow>();
  // a card a task's supervisor holds is not the user's yet (lib/supervisor-held.ts), nor is an agent waiting only on such
  const cards = [...new Map(userCards(interactions).map((ix) => [ix.id, ix])).values()];
  const held = supervisorHeldOnly(interactions);
  for (const ix of cards.sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? "") || a.id.localeCompare(b.id))) {
    // a child session's card is its parent's (#5120): one row per parent, opening the parent
    const homeMid = requestHomeMachine(ix);
    const key = ix.session_id ? sessionKey(homeMid, requestHome(ix)) : `request:${ix.id}`;
    const existing = rows.get(key);
    if (existing) {
      existing.cards.push(ix);
      continue;
    }
    const to = requestRoute(ix);
    // A remembered terminal view cannot answer a card; the conversation can.
    rows.set(key, { key, machineId: homeMid, session: bySession.get(key), cards: [ix], to: to.name === "session" ? withWorkView({ ...to, view: "chat" }, true) : to });
  }
  const waiting = answeredSessions(sessions, cards, answered)
    .filter((s) => stateOf(s) === "needs_you");
  for (const s of waiting) {
    const homeMid = requestHomeMachine(s);
    const home = requestHome(s);
    const key = sessionKey(homeMid, home);
    if (held.has(`${homeMid}/${home}`)) continue;
    if (!rows.has(key)) rows.set(key, { key, machineId: homeMid, session: bySession.get(key) ?? s, cards: [], to: { name: "session", mid: homeMid, sid: home, view: "chat", work: true } });
  }
  // Urgency class first, then the longest wait within it (#8167); the row set above is unchanged by the ordering.
  return orderPending([...rows.values()]);
}
