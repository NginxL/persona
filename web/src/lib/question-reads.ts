// Which of a Persona chat's answers the owner opened (#13021), kept on the hub so every device agrees:
// `GET /api/v1/sessions/{mid}/{sid}/question-reads` → `{ since, ids }`, `POST` the same path `{ ids }` to add marks
// (docs/protocol/protocol.md, "Question reads"). A mark shows here at once and goes to the hub behind it (principle 7);
// one the hub refused is sent again with the next. A hub from before answers 404: the marks then live in this page only,
// starting when the chat opened, so nothing older shows unread.
import { useCallback, useEffect, useSyncExternalStore } from "react";
import { api, ApiError, sessionPath } from "./api";
import type { QuestionMark, QuestionReads, QuestionSummary } from "./question-states";
import { summarizeQuestions } from "./question-states";
import { timeMs } from "./times";

interface Book {
  since: number;
  ids: Set<string>;
  /** Marked here and not yet taken by the hub. */
  unsent: Set<string>;
  /** The hub keeps no marks (an older hub): this page's only. */
  local: boolean;
  loaded: boolean;
  /** A new object per change, for useSyncExternalStore. */
  view: QuestionReads;
}

const books = new Map<string, Book>();
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** Marks sent in one request at most; the hub refuses more. */
export const QUESTION_READS_BATCH = 200;

function bookOf(scope: string): Book {
  let b = books.get(scope);
  if (!b) {
    const since = Date.now();
    b = { since, ids: new Set(), unsent: new Set(), local: false, loaded: false, view: { ids: new Set(), since } };
    books.set(scope, b);
  }
  return b;
}

function changed(b: Book) {
  b.view = { ids: new Set(b.ids), since: b.since };
  emit();
}

/** Reads the chat's marks from the hub, once (again after a failed read). */
export async function loadQuestionReads(mid: string, sid: string) {
  const b = bookOf(`${mid}/${sid}`);
  if (b.loaded) return;
  b.loaded = true;
  try {
    const r = await api<{ since?: string; ids?: string[] | null }>(`${sessionPath(mid, sid)}/question-reads`);
    const since = timeMs(r.since);
    if (since) b.since = since;
    for (const id of r.ids ?? []) if (typeof id === "string") b.ids.add(id);
    changed(b);
    if (b.unsent.size) void send(mid, sid);
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 405)) b.local = true;
    else b.loaded = false; // out of reach: the next open asks again
  }
}

async function send(mid: string, sid: string) {
  const b = bookOf(`${mid}/${sid}`);
  if (b.local || !b.unsent.size) return;
  const ids = [...b.unsent].slice(0, QUESTION_READS_BATCH);
  try {
    await api(`${sessionPath(mid, sid)}/question-reads`, { method: "POST", json: { ids } });
    for (const id of ids) b.unsent.delete(id);
    if (b.unsent.size) void send(mid, sid);
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 405)) b.local = true;
    // otherwise kept unsent: the next mark sends them again
  }
}

/** Marks these questions' answers opened. */
export function markQuestionsRead(mid: string, sid: string, keys: readonly string[]) {
  const b = bookOf(`${mid}/${sid}`);
  const fresh = keys.filter((k) => !b.ids.has(k));
  if (!fresh.length) return;
  for (const k of fresh) {
    b.ids.add(k);
    b.unsent.add(k);
  }
  changed(b);
  if (b.loaded) void send(mid, sid);
}

const OFF: QuestionReads = { ids: new Set(), since: Number.MAX_SAFE_INTEGER };

/** The chat's read marks (loaded from the hub on first use) and a way to add some. Off: nothing is read or unread. */
export function useQuestionReads(mid: string, sid: string, on: boolean): { reads: QuestionReads; markRead: (keys: readonly string[]) => void } {
  const scope = `${mid}/${sid}`;
  useEffect(() => {
    if (on) void loadQuestionReads(mid, sid);
  }, [mid, sid, on]);
  const snapshot = () => (on ? bookOf(scope).view : OFF);
  const reads = useSyncExternalStore(subscribe, snapshot, snapshot);
  const markRead = useCallback((keys: readonly string[]) => markQuestionsRead(mid, sid, keys), [mid, sid]);
  return { reads, markRead };
}

// ---- the summary: the same marks the rail draws, for a place outside the chat ----

const summaries = new Map<string, QuestionSummary>();
const EMPTY: QuestionSummary = { working: 0, unread: 0, total: 0 };

/** The chat publishes its marks while it is open (ChatView); the summary reads them. */
export function publishQuestionMarks(scope: string, marks: ReadonlyMap<string, QuestionMark> | undefined) {
  const next = summarizeQuestions(marks);
  const was = summaries.get(scope);
  if (was && was.working === next.working && was.unread === next.unread && was.total === next.total) return;
  if (next.total) summaries.set(scope, next);
  else summaries.delete(scope);
  emit();
}

/** A chat's summary now (all zero while it is not open). */
export const summaryOf = (scope: string | undefined): QuestionSummary => (scope ? summaries.get(scope) ?? EMPTY : EMPTY);

/** The counts of a chat's questions in progress and unread, as its rail shows them (all zero while it is not open). */
export function useQuestionSummary(scope: string | undefined): QuestionSummary {
  const snapshot = () => summaryOf(scope);
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

/** A chat's marks as the hook gives them. */
export const questionReadsOf = (mid: string, sid: string): QuestionReads => bookOf(`${mid}/${sid}`).view;

/** Tests only: forget every chat's marks. */
export function resetQuestionReads() {
  books.clear();
  summaries.clear();
  emit();
}
