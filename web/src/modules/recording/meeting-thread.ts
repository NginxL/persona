// A recorded meeting's one thread (#3862, docs/assistant/meeting-minutes.md "One thread"): the room conversation's items,
// oldest first, then the follow-up's, read apart into what the recording detail shows. The room's first message is the
// meeting header, its batches carry message source `meeting` and collapse to "N lines from the meeting", a heard
// question shows as "What was just said (m:ss)", a photo forwarded into the room (phase 3: its image and its text, both
// `photo-<id>`) is one entry, and the follow-up's first message holds the meeting's material in a context block before
// the owner's words. Pure: no DOM, no requests.
import type { KBDetail, LiveLine, MeetingRoom, MeetingTopic, RecordingAsk } from "../../lib/kb";
import { parseUserText } from "../../lib/turns";
import type { Item } from "../../lib/types";

/** The hub takes the meeting conversation's phase 1 (protocol.md "Added by the hub"). */
export const MEETING_CONVERSATION_FEATURE = "meeting_conversation";
/** The hub takes meeting photos (phase 3, "Phase 3: photos"); it comes only with `meeting_conversation`. */
export const MEETING_PHOTOS_FEATURE = "meeting_photos";
/** The hub gives the live view (phase 2, "Phase 2: live view"): `lines` and `topics` on `GET …/live`; it comes only with
 *  `meeting_conversation`. */
export const MEETING_LIVE_VIEW_FEATURE = "meeting_live_view";

/** One live line of a batch or a heard question: its clock in the meeting (`m:ss`) and its words. */
export interface MeetingLine {
  clock: string;
  text: string;
}

export type ThreadEntry =
  /** A batch of live lines. */
  | { kind: "lines"; id: string; ts?: string; lines: MeetingLine[] }
  /** The end batch: the meeting ended (after the batch's last lines, when it had any). */
  | { kind: "ended"; id: string; ts?: string }
  /** A question heard in the room: the lines before the tap; `ask` the ask it is an attempt of, lowercase. */
  | { kind: "heard"; id: string; ts?: string; clock: string; lines: MeetingLine[]; ask?: string }
  /** The owner's own message: an ask typed or spoken on the phone (`ask`, lowercase), or written in the follow-up. */
  | { kind: "question"; id: string; ts?: string; text: string; via: "typed" | "spoken" | "owner"; ask?: string }
  | { kind: "answer"; id: string; ts?: string; text: string }
  | { kind: "image"; id: string; ts?: string }
  /** A meeting photo forwarded into the room: `photo` its id, lowercase, which `GET …/photos` resolves (never the room's
   *  own copy of the image). */
  | { kind: "photo"; id: string; ts?: string; photo: string }
  /** A context block of the owner's message; the follow-up's first (`seed`) is the meeting's material. */
  | { kind: "material"; id: string; ts?: string; title: string; text: string; seed: boolean };

const LEADING_SOURCE = /^\s*<persona-context\b([^>]*)>([\s\S]*?)<\/persona-context>/;
const BLOCK_END = "</persona-context>";
const LINE = /^\[(\d{1,2}(?::\d{2}){1,2})\]\s?(.*)$/;
const ASK_MSG = /^ask-([0-9a-f-]{36})-\d+$/;
const UUID = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
const PHOTO_MSG = new RegExp(`^photo-(${UUID})$`);
const WITHDRAWN_MSG = new RegExp(`^photo-withdrawn-(${UUID})$`);

/** Whether a user line came from the meeting (message source `meeting`): its `source`, else the leading message-source
 *  block the manager persists, which the hub reads only for lines without a client_msg_id (the header and a heard
 *  question carry one). */
export function fromMeeting(it: Item): boolean {
  if (it.source?.kind) return it.source.kind === "meeting";
  const m = LEADING_SOURCE.exec(it.text ?? "");
  if (!m || !/\bkind="message-source"/.test(m[1])) return false;
  try {
    const meta: unknown = JSON.parse(m[2].trim());
    return !!meta && typeof meta === "object" && (meta as { kind?: unknown }).kind === "meeting";
  } catch {
    return false;
  }
}

/** The `[m:ss] text` lines of a context block, in order; anything else in it (a note of lines left out) is not one. */
export function meetingLines(text: string): MeetingLine[] {
  const out: MeetingLine[] = [];
  for (const raw of text.split("\n")) {
    const m = LINE.exec(raw.trim());
    if (m && m[2].trim()) out.push({ clock: m[1], text: m[2].trim() });
  }
  return out;
}

/** Seconds from the start as the hub's meeting clock: `m:ss`, `h:mm:ss`. */
export function meetingClock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return h ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

/** The ask an attempt's message belongs to (`ask-<id>-<k>`, "Asking the room"), lowercase. */
function askOf(clientMsgID: string | undefined): string | undefined {
  return ASK_MSG.exec((clientMsgID ?? "").toLowerCase())?.[1];
}

/** The photo a room message forwarded (`photo-<id>`, "Into the room"), lowercase. */
export function photoOf(clientMsgID: string | undefined): string | undefined {
  return PHOTO_MSG.exec((clientMsgID ?? "").toLowerCase())?.[1];
}

/** The room's items as thread entries. `recordingId` names the header (`meeting-<recording id>`); `asks` tell a spoken
 *  question from a typed one and give a heard question's time when its block has none. */
export function roomThread(items: Item[], recordingId: string, asks: RecordingAsk[] = []): ThreadEntry[] {
  const header = `meeting-${recordingId}`.toLowerCase();
  const byId = new Map(asks.map((a) => [a.id.toLowerCase(), a]));
  const photos = new Set<string>();
  const out: ThreadEntry[] = [];
  for (const it of items) {
    // A photo's image and its text are one entry, read before anything else: its text is from the meeting and would
    // otherwise read as a batch (or the end batch). The line withdrawing a deleted photo is the hub's to the room only.
    const photo = it.role === "user" ? photoOf(it.client_msg_id) : undefined;
    if (photo) {
      if (!photos.has(photo)) out.push({ kind: "photo", id: it.id, ts: it.ts, photo });
      photos.add(photo);
      continue;
    }
    if (it.role === "user" && WITHDRAWN_MSG.test((it.client_msg_id ?? "").toLowerCase())) continue;
    if (it.role === "user" && it.kind === "text") {
      const sourced = fromMeeting(it);
      const ask = askOf(it.client_msg_id);
      const { text, contexts } = parseUserText(it.text ?? "");
      const block = contexts.find((c) => c.kind === "context");
      if ((it.client_msg_id ?? "").toLowerCase() === header || (sourced && block && isHeader(block.text))) {
        // The header shows nothing (#9411): a meeting begun with Persona as the note-taker had nobody join, and Persona
        // invited in or taken out later is the phone's to know, so the web says neither.
        continue;
      } else if (sourced && (ask || /^What was just said\b/.test(block?.title ?? ""))) {
        const at = byId.get(ask ?? "")?.at;
        const clock = /\(([\d:]+)\)\s*$/.exec(block?.title ?? "")?.[1] ?? (typeof at === "number" ? meetingClock(at) : "");
        out.push({ kind: "heard", id: it.id, ts: it.ts, clock, lines: meetingLines(block?.text ?? ""), ...(ask ? { ask } : {}) });
      } else if (sourced) {
        // A batch (prompts/meeting/feed.md): its lines in the block. One that holds no line (only questions answered
        // from the meeting's material, which show among the asks) is left out; the end batch says the meeting ended
        // after its block, or alone when nothing was new.
        const lines = meetingLines(block?.text ?? "");
        if (lines.length) out.push({ kind: "lines", id: it.id, ts: it.ts, lines });
        if (endBatch(it.text ?? "")) out.push({ kind: "ended", id: `${it.id}#end`, ts: it.ts });
      } else if (!it.source?.kind && !it.injected && text) {
        // The owner's own: an ask (typed, or spoken from a Talk span), or a message an app sent into the room.
        const via = ask ? (byId.get(ask)?.question_source === "spoken" ? "spoken" : "typed") : "owner";
        out.push({ kind: "question", id: it.id, ts: it.ts, text, via, ...(ask ? { ask } : {}) });
      }
    } else {
      pushReply(out, it);
    }
  }
  return out;
}

/** The follow-up's items as thread entries: the first message's context block is the meeting's material (shown
 *  folded), then the owner's words; later context blocks are the owner's own picks. */
export function followupThread(items: Item[]): ThreadEntry[] {
  const out: ThreadEntry[] = [];
  let seeded = false;
  for (const it of items) {
    if (it.role === "user" && it.kind === "text") {
      if (it.source?.kind || it.injected || fromMeeting(it)) continue; // Persona's own lines, not the owner's
      const { text, contexts } = parseUserText(it.text ?? "");
      contexts.filter((c) => c.kind === "context").forEach((c, i) => {
        out.push({ kind: "material", id: `${it.id}#c${i}`, ts: it.ts, title: c.title ?? "", text: c.text, seed: !seeded });
        seeded = true;
      });
      seeded = true;
      if (text) out.push({ kind: "question", id: it.id, ts: it.ts, text, via: "owner" });
    } else {
      pushReply(out, it);
    }
  }
  return out;
}

/** A reply's text, joined to the one before it when nothing came between; a photo the owner sent. */
function pushReply(out: ThreadEntry[], it: Item) {
  if (it.role === "assistant" && it.kind === "text" && it.text?.trim()) {
    const last = out.at(-1);
    if (last?.kind === "answer") out[out.length - 1] = { ...last, text: `${last.text}\n\n${it.text.trim()}` };
    else out.push({ kind: "answer", id: it.id, ts: it.ts, text: it.text.trim() });
  } else if (it.role === "user" && it.kind === "image" && !it.tool_use_id) {
    out.push({ kind: "image", id: it.id, ts: it.ts });
  }
}

/** Whether a batch is the end batch: words after its last block (every other batch ends with its lines' block), or no
 *  block of its own (the end with nothing new: only its message-source block, or none). */
function endBatch(raw: string): boolean {
  const end = raw.lastIndexOf(BLOCK_END);
  return end < 0 || raw.slice(end + BLOCK_END.length).trim() !== "";
}

/** The meeting header's block (prompts/meeting/room_header.md): it says when it started and the role. */
function isHeader(text: string): boolean {
  return /^Started: /m.test(text) && /^Role: /m.test(text);
}

/** Room states the apps know ("States"); any other reads as `starting`. */
const ROOM_STATES = ["none", "starting", "ready", "unavailable", "ended", "deleted"];

/** The room's state as the apps read it: an unknown one is `starting`. */
export function roomState(room: MeetingRoom | undefined): string | undefined {
  if (!room) return undefined;
  return ROOM_STATES.includes(room.state) ? room.state : "starting";
}

/** How long a meeting that never sends `ended_at` counts as running after its last upload (the hub's feedStale). */
const STALE_MS = 12 * 3600_000;

/** Whether the meeting is still running for a follow-up (the hub's `409 meeting_running`): no `ended_at` and an upload
 *  in the last 12 hours, or a room still starting, ready or unavailable. A trimmed recording has ended. */
export function meetingRunning(meta: Record<string, unknown> | null | undefined, room: MeetingRoom | undefined, now = Date.now()): boolean {
  if (typeof meta?.trimmed_at === "string" && meta.trimmed_at) return false;
  if (!(typeof meta?.ended_at === "string" && Number.isFinite(Date.parse(meta.ended_at)))) {
    const updated = typeof meta?.updated === "string" ? Date.parse(meta.updated) : NaN;
    if (!Number.isFinite(updated) || now - updated < STALE_MS) return true;
  }
  const state = roomState(room);
  return state === "starting" || state === "ready" || state === "unavailable";
}

/** Whether a recording has something to talk about (phase 6, "Phase 6: talk about this meeting"): text or minutes, as
 *  the hub's `409 no_material` counts them (its `chars`, else a line of its transcript that is not Persona's echo, or its
 *  minutes). A trimmed recording has only its minutes. */
export function hasMaterial(detail: Pick<KBDetail, "kind" | "meta" | "segments" | "minutes"> | null | undefined): boolean {
  if (detail?.kind !== "recordings") return false;
  if (detail.minutes) return true;
  const meta = detail.meta;
  if (typeof meta?.trimmed_at === "string" && meta.trimmed_at) return false;
  if (typeof meta?.chars === "number" && meta.chars > 0) return true;
  return (detail.segments ?? []).some((s) => s?.echo !== true && typeof s?.text === "string" && s.text.trim() !== "");
}

/** Whether a recording's detail offers **Talk about this meeting** (phase 6): a recording with something to talk about
 *  (`hasMaterial`) that is not an all-day one (kind `day`), whose page is its day recap, as the iPhone leaves it out. The
 *  hub refuses no kind, so a follow-up made for one still shows its thread. */
export function offersTalk(detail: Pick<KBDetail, "kind" | "meta" | "segments" | "minutes"> | null | undefined): boolean {
  return hasMaterial(detail) && detail?.meta?.kind !== "day";
}

/** The parts of a `meeting_live` event newer than the revisions read so far (a part without a number is read). */
export function newerParts(changed: unknown, revs: unknown, known: Record<string, number>): string[] {
  if (!Array.isArray(changed)) return [];
  const given = revs && typeof revs === "object" ? (revs as Record<string, unknown>) : {};
  const out: string[] = [];
  for (const part of changed) {
    if (typeof part !== "string" || out.includes(part)) continue;
    const rev = given[part];
    if (typeof rev !== "number" || !(part in known) || rev > known[part]) out.push(part);
  }
  return out;
}

/** How many of the newest live lines the detail keeps (the transcript below has the rest). */
export const LIVE_KEEP = 200;

/** Where the next read of live lines starts (`?from=`): the reader's own next position, but never more than `keep`
 *  lines behind the live log's length (`revs.lines`), so a long meeting is never read from its first line. */
export function linesFrom(next: number | undefined, length: number, keep = LIVE_KEEP): number {
  return Math.max(next ?? 0, length - keep, 0);
}

/** Live lines read so far and the new ones, by position, the newest `keep`. */
export function mergeLiveLines(have: readonly LiveLine[], add: readonly LiveLine[], keep = LIVE_KEEP): LiveLine[] {
  if (!add.length) return have as LiveLine[];
  const byN = new Map<number, LiveLine>();
  for (const l of have) byN.set(l.n, l);
  for (const l of add) byN.set(l.n, l);
  const out = [...byN.values()].sort((a, b) => a.n - b.n);
  return out.length > keep ? out.slice(out.length - keep) : out;
}

/** `lines` of `GET …/live` read defensively, by position: entries without a whole `n` of 0 or more or without words are
 *  left out, a position given twice counts once. */
export function liveLines(v: unknown): LiveLine[] {
  if (!Array.isArray(v)) return [];
  const byN = new Map<number, LiveLine>();
  for (const x of v as Record<string, unknown>[]) {
    if (!x || typeof x !== "object" || !Number.isInteger(x.n) || (x.n as number) < 0 || typeof x.text !== "string" || !x.text.trim()) continue;
    const sec = (k: "start" | "end") => (typeof x[k] === "number" && Number.isFinite(x[k]) ? Math.max(0, x[k] as number) : 0);
    const source = text(x.source);
    byN.set(x.n as number, { n: x.n as number, start: sec("start"), end: sec("end"), text: x.text.trim(), ...(source ? { source } : {}) });
  }
  return [...byN.values()].sort((a, b) => a.n - b.n);
}

/** The position after the last one `lines` of `GET …/live` gave (placeholder positions without words included), 0 for
 *  none: where the next read of new lines starts. */
export function liveLinesEnd(v: unknown): number {
  let end = 0;
  if (Array.isArray(v)) for (const x of v as Record<string, unknown>[]) if (x && Number.isInteger(x.n) && (x.n as number) >= 0) end = Math.max(end, (x.n as number) + 1);
  return end;
}

/** `topics` of `GET …/live` read defensively, in the room's order: entries without a title are left out, an unknown
 *  state reads as `open`, and a second `current` as `open` too. */
export function meetingTopics(v: unknown): MeetingTopic[] {
  if (!Array.isArray(v)) return [];
  const out: MeetingTopic[] = [];
  let current = false;
  for (const x of v as Record<string, unknown>[]) {
    const title = x && typeof x === "object" ? text(x.title)?.trim() : undefined;
    if (!title) continue;
    const given = text(x.state)?.trim().toLowerCase();
    let state: MeetingTopic["state"] = given === "done" || given === "current" ? given : "open";
    if (state === "current" && current) state = "open";
    if (state === "current") current = true;
    const note = text(x.note)?.trim();
    out.push({
      title, state,
      ...(typeof x.at === "number" && Number.isFinite(x.at) ? { at: Math.max(0, x.at) } : {}),
      ...(note ? { note } : {}),
    });
  }
  return out;
}

/** A string value, or nothing. */
function text(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}
