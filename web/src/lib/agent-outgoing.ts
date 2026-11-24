// Full-text recovery for personal Agent sends. The hub outbox is authoritative metadata, but its 200-character preview
// cannot restore a message. Keep the complete local entry under the established hub + credential storage scope, and
// never put credentials in a new key or value (#12555).
import { t } from "../i18n";
import { credentialOwner } from "./api";
import { removeStoredValue, storedValue, storeValue } from "./compatibility";
import { feedCacheScope, scopedKey } from "./feed-cache";
import { pendingOutgoing, type AgentMessage, type AgentOutboxEntry, type Outgoing } from "./agents";

const STORAGE_NAME = "agent-outgoing-v1";
const MAX_KEPT_SENT = 20;
const MAX_DISCARDED = 500; // the hub's outbox bound; ids only, never message text

export interface PersonalOutgoingStore {
  readonly name: string;
  readonly key: string | null;
  readonly owner: string;
  /** Every id this mount has held. A stored entry outside it belongs to another tab and is kept on write. */
  readonly seen: Set<string>;
}

interface StoredOutgoing {
  entries: Outgoing[];
  discarded: Record<string, number>;
}

const EMPTY: StoredOutgoing = { entries: [], discarded: {} };
const sameEntries = (a: Outgoing[], b: Outgoing[]) => a.length === b.length && a.every((x, i) => x === b[i]);

/** Capture once for a mounted conversation. Async completions keep this scope rather than following a later sign-in. */
export function personalOutgoingStore(id: string): PersonalOutgoingStore {
  const name = `${STORAGE_NAME}:${encodeURIComponent(id)}`;
  const scope = feedCacheScope();
  return { name, key: scopedKey(scope, name), owner: credentialOwner(), seen: new Set() };
}

export function personalOutgoingStoreCurrent(store: PersonalOutgoingStore): boolean {
  if (store.owner !== credentialOwner()) return false;
  try {
    return scopedKey(feedCacheScope(), store.name) === store.key;
  } catch {
    return false;
  }
}

function validOutgoing(value: unknown): value is Outgoing {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const x = value as Partial<Outgoing>;
  return typeof x.clientId === "string" && Boolean(x.clientId) && typeof x.text === "string"
    && typeof x.at === "number" && Number.isFinite(x.at)
    && (x.updatedAt === undefined || (typeof x.updatedAt === "number" && Number.isFinite(x.updatedAt)))
    && (x.status === "sending" || x.status === "sent" || x.status === "failed")
    && (x.error === undefined || typeof x.error === "string")
    && (x.attempts === undefined || (typeof x.attempts === "number" && Number.isSafeInteger(x.attempts) && x.attempts >= 0));
}

function discardedOf(value: unknown): Record<string, number> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const valid = Object.entries(value).filter((entry): entry is [string, number] => Boolean(entry[0]) && typeof entry[1] === "number" && Number.isFinite(entry[1]));
  valid.sort((a, b) => b[1] - a[1]);
  return Object.fromEntries(valid.slice(0, MAX_DISCARDED));
}

function readOutgoing(store: PersonalOutgoingStore): StoredOutgoing {
  if (!store.key || !personalOutgoingStoreCurrent(store)) return EMPTY;
  try {
    const raw = JSON.parse(storedValue(store.key) ?? "null") as { version?: number; entries?: unknown; discarded?: unknown } | null;
    if (!raw || raw.version !== 1 || !Array.isArray(raw.entries)) return EMPTY;
    const discarded = discardedOf(raw.discarded);
    const byId = new Map<string, Outgoing>();
    for (const value of raw.entries) {
      if (!validOutgoing(value) || discarded[value.clientId]) continue;
      const entry = { ...value, updatedAt: value.updatedAt ?? value.at };
      byId.set(entry.clientId, entry);
    }
    return { entries: [...byId.values()], discarded };
  } catch {
    return EMPTY;
  }
}

function writeOutgoing(store: PersonalOutgoingStore, entries: Outgoing[], discarded: Record<string, number>): void {
  if (!store.key || !personalOutgoingStoreCurrent(store)) return;
  try {
    if (!entries.length && !Object.keys(discarded).length) removeStoredValue(store.key);
    else storeValue(store.key, JSON.stringify({ version: 1, entries, discarded }));
  } catch {
    // Storage can be blocked or full. The mounted view retains its in-memory copy, as the other Web outboxes do.
  }
}

/** Accepted bubbles are bounded; failed and otherwise unconfirmed input is never evicted automatically. */
function pruneSent(entries: Outgoing[]): Outgoing[] {
  const sent = entries.filter((x) => x.status === "sent").sort((a, b) => b.updatedAt - a.updatedAt || b.at - a.at);
  const keep = new Set(sent.slice(0, MAX_KEPT_SENT).map((x) => x.clientId));
  const next = entries.filter((x) => x.status !== "sent" || keep.has(x.clientId));
  return next.length === entries.length ? entries : next;
}

/** Persist before the matching state update returns to the composer. Existing discard tombstones always win. */
export function savePersonalOutgoing(store: PersonalOutgoingStore, entries: Outgoing[]): Outgoing[] {
  if (!personalOutgoingStoreCurrent(store)) return [];
  const saved = readOutgoing(store);
  const kept = entries.filter((x) => !saved.discarded[x.clientId]);
  for (const x of entries) store.seen.add(x.clientId);
  const next = pruneSent(kept);
  // Another tab's unconfirmed or failed input stays stored for its reload; this mount neither shows nor settles it.
  const foreign = saved.entries.filter((x) => !store.seen.has(x.clientId));
  writeOutgoing(store, foreign.length ? pruneSent([...next, ...foreign]) : next, saved.discarded);
  return next;
}

/** Restore locally without resending. A page that closed during POST exposes an explicit same-id Retry instead. */
export function restorePersonalOutgoing(store: PersonalOutgoingStore, now = Date.now()): Outgoing[] {
  const saved = readOutgoing(store);
  for (const x of saved.entries) store.seen.add(x.clientId);
  let changed = false;
  const entries = saved.entries.map((entry) => {
    if (entry.status !== "sending") return entry;
    changed = true;
    return { ...entry, status: "failed" as const, error: entry.error || t("lib-outbox.not_confirmed"), updatedAt: now };
  });
  if (changed) writeOutgoing(store, entries, saved.discarded);
  return entries;
}

/** Discard is local and deliberate: no server write, no later restoration from a preview or a late POST completion. */
export function discardPersonalOutgoing(store: PersonalOutgoingStore, entries: Outgoing[], clientId: string, now = Date.now()): Outgoing[] {
  if (!personalOutgoingStoreCurrent(store)) return [];
  const saved = readOutgoing(store);
  const discarded = discardedOf({ ...saved.discarded, [clientId]: now });
  const next = entries.filter((x) => x.clientId !== clientId);
  const foreign = saved.entries.filter((x) => !store.seen.has(x.clientId) && x.clientId !== clientId);
  writeOutgoing(store, [...next, ...foreign], discarded);
  return next;
}

function outgoingEqual(a: Outgoing, b: Outgoing): boolean {
  return a.clientId === b.clientId && a.text === b.text && a.at === b.at && a.updatedAt === b.updatedAt
    && a.status === b.status && a.error === b.error && a.attempts === b.attempts;
}

/** Apply one hub receipt to an entry the browser already has. Null means the hub deleted it. Unknown states change
 *  nothing; `sending` never turns a restored failure back into an endless spinner. */
export function applyPersonalOutbox(entry: Outgoing, receipt: AgentOutboxEntry): Outgoing | null {
  if (receipt.status === "deleted") return null;
  const parsed = Date.parse(receipt.updatedAt ?? "");
  const updatedAt = Number.isFinite(parsed) ? parsed : entry.updatedAt;
  const attempts = receipt.attempts ?? entry.attempts;
  let next: Outgoing;
  if (receipt.status === "sent") next = { ...entry, status: "sent", error: undefined, updatedAt, attempts };
  else if (receipt.status === "failed") next = { ...entry, status: "failed", error: receipt.error || entry.error, updatedAt, attempts };
  else if (receipt.status === "sending") next = { ...entry, updatedAt, attempts };
  else return entry;
  return outgoingEqual(entry, next) ? entry : next;
}

/** Reconcile only locally held full text. Metadata never creates a preview-only bubble. An exact accepted history id
 *  settles even an interrupted/unknown send; otherwise the existing text + clock-slack rule applies only to sent rows. */
export function reconcilePersonalOutgoing(entries: Outgoing[], messages: AgentMessage[], metadata: AgentOutboxEntry[] | null): Outgoing[] {
  const receipts = metadata === null ? undefined : new Map(metadata.map((x) => [x.clientId, x]));
  const reconciled = entries.flatMap((entry) => {
    const receipt = receipts?.get(entry.clientId);
    const next = receipt ? applyPersonalOutbox(entry, receipt) : entry;
    if (!next) return [];
    const accepted = messages.some((m) => m.role === "user" && m.local?.clientId === entry.clientId && m.local.status === "sent");
    return accepted ? [] : [next];
  });
  const pending = pendingOutgoing(reconciled, messages);
  return sameEntries(entries, pending) ? entries : pending;
}
