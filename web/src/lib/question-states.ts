// What each of your questions in a Persona chat is at (#13021): waiting its turn, being worked on, answered and not opened
// yet, or read. The chat's question rail (views/chat/NavRail.tsx) draws it on each tick, and the question summary
// (views/chat/QuestionSummary.tsx) counts the same marks: one rule, one place (docs/design/PRINCIPLES.md, principle 4).
// Pure: the chat's loaded lines, which exchange is at work, and the read marks in; the marks out.
import { navRailTicks } from "./nav-rail";
import { timeMs } from "./times";
import type { ChatLine, Segment, UserMessage } from "./turns";

/** Not started (queued behind another turn), in progress, answered but not opened, read. */
export type QuestionState = "pending" | "running" | "unread" | "read";

export interface QuestionMark {
  state: QuestionState;
  /** The read mark's key: the question's own message (its entry id, without a part's `#n`). */
  key: string;
  /** A follow-up: the line id of the question it follows (the first question of its thread). */
  follows?: string;
}

export interface QuestionReads {
  /** Questions whose answers were opened, by `QuestionMark.key`. */
  ids: ReadonlySet<string>;
  /** Answers that ended before this (ms) count as read: marks began then, so older history is not all unread. */
  since: number;
}

/** The shortest quoted words that tie a follow-up to an answer: shorter ones ("ok", "yes") match too much. */
export const FOLLOW_QUOTE_MIN = 8;

const entry = (id: string) => id.split("#")[0];
const fold = (s: string) => s.replace(/\s+/g, " ").trim().toLowerCase();

/** The words a question quotes: its `> ` lines and the text of what it carries as context (selected text sent with "Use
 *  as context"), each folded. */
function quotesOf(msg: UserMessage): string[] {
  const out: string[] = [];
  const quoted = msg.text.split("\n").filter((l) => l.trimStart().startsWith(">")).map((l) => l.replace(/^\s*>\s?/, "")).join(" ");
  if (fold(quoted).length >= FOLLOW_QUOTE_MIN) out.push(fold(quoted));
  for (const c of msg.contexts) if (fold(c.text).length >= FOLLOW_QUOTE_MIN) out.push(fold(c.text));
  return out;
}

/** When the exchange ended (ms): its answer's time, else its last time; 0 when nothing says. */
function endedAt(seg: Segment): number {
  return timeMs(seg.answer?.ts) || timeMs(seg.end) || timeMs(seg.start) || 0;
}

interface Asked { line: string; root: string; answer: string; ended: number; open: boolean }

/** Which earlier question `msg` follows, by the line id of that thread's first question:
 *  1. it quotes words from an earlier answer (a `> ` quote or a picked context), the latest such answer; or
 *  2. it was sent while the question before it was still being worked on (added before its answer came).
 *  Otherwise it is a question of its own. */
export function followOf(msg: UserMessage, earlier: readonly Asked[]): string | undefined {
  const quotes = quotesOf(msg);
  if (quotes.length) {
    for (let i = earlier.length - 1; i >= 0; i--) {
      const answer = earlier[i].answer;
      if (answer && quotes.some((q) => answer.includes(q))) return earlier[i].root;
    }
  }
  const prev = earlier.at(-1);
  const sent = timeMs(msg.ts);
  if (prev && sent && (prev.open || (prev.ended && sent < prev.ended))) return prev.root;
  return undefined;
}

/** Each question's mark, by its line id, in the order asked. `busy` is the line id of the exchange at work (ChatView's
 *  busySegment), absent while the chat is idle.
 *  - pending: the message waits in the queue (or comes after the one at work);
 *  - running: the exchange at work;
 *  - unread: it ended after `reads.since` and its answer has not been opened;
 *  - read: opened, or ended before marks began. */
export function questionMarks(lines: readonly ChatLine[], busy: string | undefined, reads: QuestionReads): Map<string, QuestionMark> {
  const out = new Map<string, QuestionMark>();
  const asked = new Set(navRailTicks(lines as ChatLine[]).map((tick) => tick.id));
  const earlier: Asked[] = [];
  let pastBusy = false;
  for (const line of lines) {
    if (line.kind !== "segment") continue;
    const { seg } = line;
    const atWork = line.id === busy;
    const msg = seg.user;
    if (msg && asked.has(line.id)) {
      const key = entry(msg.id);
      const ended = endedAt(seg);
      const state: QuestionState = msg.queued || pastBusy ? "pending"
        : atWork ? "running"
        : reads.ids.has(key) || (ended > 0 && ended < reads.since) ? "read" : "unread";
      const follows = followOf(msg, earlier);
      out.set(line.id, { state, key, ...(follows ? { follows } : {}) });
      earlier.push({ line: line.id, root: follows ?? line.id, answer: fold(seg.answer?.text ?? ""), ended, open: state === "running" || state === "pending" });
    }
    if (atWork) pastBusy = true;
  }
  return out;
}

export interface QuestionSummary {
  /** Not started or in progress. */
  working: number;
  /** Answered, not opened. */
  unread: number;
  /** Every question with a mark. */
  total: number;
}

/** The counts the summary shows, from the same marks the rail draws. */
export function summarizeQuestions(marks: ReadonlyMap<string, QuestionMark> | undefined): QuestionSummary {
  const out: QuestionSummary = { working: 0, unread: 0, total: 0 };
  if (!marks) return out;
  for (const m of marks.values()) {
    out.total++;
    if (m.state === "pending" || m.state === "running") out.working++;
    else if (m.state === "unread") out.unread++;
  }
  return out;
}
