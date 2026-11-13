import { useMemo } from "react";
import { useInbox, type InboxItem } from "./inbox";
import { sameManager } from "./manager-choice";
import { useStore } from "./store";
import type { AssistantInfo, Session } from "./types";

export interface InboxOrigin { id: string; manager?: string; title?: string }

export interface InboxOriginInput {
  session?: Session;
  sessions: Record<string, Session[]>;
  assistant: AssistantInfo | null;
  items: InboxItem[];
  history: InboxItem[];
}

const MAX_PARENT_LINKS = 3;

function nonEmptyRef(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  return value.trim() || undefined;
}

function itemOrigin(item: InboxItem): InboxOrigin {
  return { id: item.id, manager: item.manager, title: item.title || undefined };
}

function directOrigin(session: Session, input: InboxOriginInput): InboxOrigin | undefined {
  if (session.origin !== "inbox") return undefined;
  const id = nonEmptyRef(session.origin_ref);
  if (!id) return undefined;
  const matches = [...input.items, ...input.history].filter((item) => item.id === id);
  const item = matches.find((candidate) => sameManager(candidate.manager, session.manager));
  // The origin is authoritative even before its item has loaded; another manager's copy never lends its title or address.
  return item ? itemOrigin(item) : { id, manager: session.manager, title: undefined };
}

function parentSession(session: Session, mid: string, input: InboxOriginInput): Session | undefined {
  if (session.origin !== "spawned") return undefined;
  const id = nonEmptyRef(session.origin_ref);
  if (!id) return undefined;
  const matches: Session[] = [];
  for (const rows of Object.values(input.sessions)) {
    for (const row of rows) if (row.session_id === id) matches.push(row);
  }
  for (const row of input.assistant?.conversations ?? []) {
    if (row.session_id === id) matches.push(row);
  }
  return matches.find((row) => row.machine_id === mid && sameManager(row.manager, session.manager))
    ?? matches.find((row) => row.machine_id === mid)
    ?? matches.find((row) => sameManager(row.manager, session.manager))
    ?? matches[0];
}

function sessionKey(mid: string, sid: string): string {
  return JSON.stringify([mid, sid]);
}

function matchesSession(item: InboxItem, mid: string, sid: string): boolean {
  if (item.handling?.machine_id === mid && item.handling.session_id === sid) return true;
  return Boolean(item.chain?.some((entry) => {
    const link = entry.link as Record<string, unknown> | undefined;
    return link?.type === "session" && link.machine_id === mid && link.session_id === sid;
  }));
}

function linkedOrigin(mid: string, sid: string, input: InboxOriginInput): InboxOrigin | undefined {
  const linked = [...input.items, ...input.history].filter((item) => matchesSession(item, mid, sid));
  const session = input.session;
  const own = session ? linked.filter((item) => sameManager(item.manager, session.manager)) : [];
  // A session under a non-default manager never follows another manager's item. Without a manager name (older hubs), an
  // exact link stays usable; a default-manager copy still wins over another manager's when both name this session.
  const candidates = own.length ? own : session?.manager ? [] : linked;
  let best: InboxItem | undefined;
  let bestTime = -Infinity;
  for (const item of candidates) {
    const parsed = typeof item.updated_at === "string" ? Date.parse(item.updated_at) : NaN;
    const time = Number.isFinite(parsed) ? parsed : -Infinity;
    // Strict comparison preserves input order for equal and invalid dates.
    if (!best || time > bestTime) {
      best = item;
      bestTime = time;
    }
  }
  return best ? itemOrigin(best) : undefined;
}

export function inboxOriginForSession(mid: string, sid: string, input: InboxOriginInput): InboxOrigin | undefined {
  const visited = new Set([sessionKey(mid, sid)]);
  let row = input.session;
  // The current row plus three parents: ancestors use origin rules only.
  for (let depth = 0; row && depth <= MAX_PARENT_LINKS; depth++) {
    const direct = directOrigin(row, input);
    if (direct) return direct;
    const parent = parentSession(row, mid, input);
    if (!parent || depth === MAX_PARENT_LINKS) break;
    const key = sessionKey(parent.machine_id, parent.session_id);
    if (visited.has(key)) break;
    visited.add(key);
    row = parent;
  }
  return linkedOrigin(mid, sid, input);
}

export function useInboxOrigin(mid: string, sid: string, session: Session | undefined): InboxOrigin | undefined {
  const sessions = useStore((state) => state.sessions);
  const assistant = useStore((state) => state.assistant);
  const items = useInbox((state) => state.items);
  const history = useInbox((state) => state.history);
  return useMemo(() => inboxOriginForSession(mid, sid, { session, sessions, assistant, items, history }),
    [mid, sid, session, sessions, assistant, items, history]);
}
