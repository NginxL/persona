// What a working Claude's status line says right now (the hub's `live` push, feature session_live; docs/protocol/protocol.md
// "Live state"). The hub keeps nothing and replays nothing: the page has what arrived while it was subscribed.
import { useEffect, useState, useSyncExternalStore } from "react";
import { stateOf } from "./format";
import { isResumed } from "./reconnect";
import { getSession, onHubEvent, useStore } from "./store";
import type { LiveEvent, SessionLive } from "./types";

/** A status line not read again for this long is gone (the node stopped reading, or the null was lost). */
export const LIVE_STALE_MS = 15_000;

export interface LiveEntry {
  live: SessionLive;
  /** When it arrived (Date.now()): the node's `at` is another machine's clock. */
  at: number;
}

const entries = new Map<string, LiveEntry>();
const listeners = new Set<() => void>();
const key = (mid: string, sid: string) => `${mid}/${sid}`;

const notify = () => listeners.forEach((l) => l());

/** Drops ordinary lines that went stale: their null may never come, and nothing shows them any more. A retry can
 *  wait minutes between attempts, so age alone must not expire it; normal work, null, unwatch or turn end clears it. */
function pruneStale(now: number): void {
  for (const [k, e] of entries) if (!e.live.retry_error && now - e.at >= LIVE_STALE_MS) entries.delete(k);
}

/** Takes one `live` push. */
export function receiveLive(msg: Pick<LiveEvent, "machine_id" | "session_id" | "live">, now = Date.now()): void {
  if (!msg.machine_id || !msg.session_id) return;
  const k = key(msg.machine_id, msg.session_id);
  // Pruning alone notifies nobody: a stale line shows nothing already (shownLive), so a null for one is a no-op too.
  pruneStale(now);
  if (msg.live) entries.set(k, { live: msg.live, at: now });
  else if (!entries.delete(k)) return;
  notify();
}

/** Forgets one session when its watch ends, before a later open can show an obsolete retry. */
export function clearLive(mid: string, sid: string): void {
  if (entries.delete(key(mid, sid))) notify();
}

/** The last line that arrived for a session (stale or not). */
export function liveEntry(mid: string, sid: string): LiveEntry | undefined {
  return entries.get(key(mid, sid));
}

/** What to show: nothing once the session no longer works. Ordinary lines expire when stale; a retry can wait minutes
 *  between attempts and stays until normal work, null, unwatch or turn end says it is over. With the hub out of reach
 *  (`online` false) no line can arrive, so silence says nothing: the last one stays, as the last state known. */
export function shownLive(entry: LiveEntry | undefined, working: boolean, now: number, online = true): SessionLive | null {
  if (!entry || !working || (!entry.live.retry_error && online && now - entry.at >= LIVE_STALE_MS)) return null;
  return entry.live;
}

onHubEvent("live", (m) => receiveLive(m as unknown as LiveEvent));
// A connection that did not resume missed every push in between, the nulls included: start empty.
onHubEvent("hello", (m) => {
  if (isResumed(m) || entries.size === 0) return;
  entries.clear();
  notify();
});

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
};

/** The session's status line while it works and the node keeps reading it; null otherwise. */
export function useSessionLive(mid: string, sid: string): SessionLive | null {
  const working = useStore(() => {
    const s = getSession(mid, sid);
    return Boolean(s && stateOf(s) === "working");
  });
  const online = useStore((s) => s.conn === "online");
  const entry = useSyncExternalStore(subscribe, () => liveEntry(mid, sid), () => undefined);
  // re-render when it goes stale
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!entry || entry.live.retry_error) return;
    const t = window.setTimeout(() => setTick((n) => n + 1), Math.max(0, entry.at + LIVE_STALE_MS - Date.now()) + 10);
    return () => clearTimeout(t);
  }, [entry]);
  return shownLive(entry, working, Date.now(), online);
}

/** Tests: forget every line. */
export function resetLive(): void {
  entries.clear();
}
