// "All chats" (docs/assistant/assistant.md "Managing its own conversations"): the assistant's conversations, the main one first, the rest newest first
// by day; search on the title and the last message; the archived ones apart. Pure functions, tested without a DOM.
import type { Session } from "../../lib/types";
import { sameManager } from "../../lib/manager-choice";
import { t } from "../../i18n";

/** A conversation as `GET /api/v1/assistant?archived=1` lists it. */
export type Conversation = Session & { archived?: boolean };

/** The write rules the hub enforces (server's conversationTagMaxRunes / conversationTagsMax), mirrored so the editor
 *  refuses what the API would. */
export const TAG_MAX_LENGTH = 32;
export const TAGS_MAX = 10;

/** Trim, drop empties and de-duplicate to the first occurrence, exactly as the hub's normalization does. */
export function normalizeTags(input: string[]): string[] {
  const out: string[] = [];
  for (const raw of input) {
    const tag = raw.trim();
    if (tag && !out.includes(tag)) out.push(tag);
  }
  return out;
}

/** "1101 | fix | search box overlaps the top bar" (docs/assistant/assistant.md "Chat titles") → head "1101 | fix | " and topic; null for other titles. */
export function splitTitle(title: string): { head: string; topic: string } | null {
  const m = /^(\d{4}\s*\|\s*[^|]+?\s*\|\s*)(.+)$/.exec(title.trim());
  return m ? { head: m[1], topic: m[2] } : null;
}

/** What a row calls it: an untitled one (empty, or Claude Code's placeholder) is "New chat". */
/** The main conversation's title: it has none of its own (the hub refuses renaming it), whatever its first message was (#1313). */
export const MAIN_TITLE = "Persona";

export function conversationTitle(s: Pick<Session, "title">, isMain = false): string {
  if (isMain) return MAIN_TITLE;
  const title = readableTitle(s.title?.trim() ?? "");
  return !title || title === "Claude Code" || title === "assistant" ? t("assistant-conversations.untitled") : title;
}

/** A chat started from picked context is titled by its first message, a `<persona-context …>` tag (docs/protocol/protocol.md
 *  "Tags in messages"): what was typed after the tag, else the tag's own title, entities decoded; empty when it has
 *  neither. Any other title as it is. */
const CONTEXT_BLOCK = /^\s*<persona-context\b[^>]*>[\s\S]*?<\/persona-context>/;
const CONTEXT_OPEN = /^<persona-context\b([^>]*)/;
const decodeAttr = (s: string) => s.replace(/&quot;/g, '"').replace(/&#39;|&apos;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");

function readableTitle(title: string): string {
  const open = CONTEXT_OPEN.exec(title);
  if (!open) return title;
  let rest = title;
  for (let m = CONTEXT_BLOCK.exec(rest); m; m = CONTEXT_BLOCK.exec(rest)) rest = rest.slice(m[0].length);
  const typed = rest === title ? "" : rest.trim().split("\n")[0].trim();
  return typed || decodeAttr(/\btitle="([^"]*)"/.exec(open[1])?.[1] ?? "").trim();
}

const fold = (s: string) => s.normalize("NFKC").toLowerCase();

/** Matches the title or the last message, ignoring case and full / half width. */
export function filterConversations<T extends Conversation>(list: T[], query: string, main?: string): T[] {
  const q = fold(query.trim());
  if (!q) return list;
  return list.filter((s) => (s.session_id === main && fold(t("sidebar.main_chat")).includes(q)) || fold(conversationTitle(s)).includes(q) || fold(s.last_text ?? "").includes(q));
}

/** Every tag in use, each once and sorted: the filter chips' row (docs/assistant/assistant.md "Conversation tags"). */
export function inUseTags<T extends Conversation>(list: T[]): string[] {
  const seen = new Set<string>();
  for (const s of list) for (const tag of s.tags ?? []) seen.add(tag);
  return [...seen].sort((a, b) => a.localeCompare(b));
}

/** Only the conversations carrying this exact tag; an empty filter is the whole list. */
export function withTag<T extends Conversation>(list: T[], tag: string): T[] {
  if (!tag) return list;
  return list.filter((s) => s.tags?.includes(tag));
}

function time(s: Session): number {
  const at = s.updated_at ? Date.parse(s.updated_at) : NaN;
  return Number.isFinite(at) && at > 86_400_000 ? at : 0;
}

/** Newest first, a fixed order for equal times. */
export function newestFirst<T extends Session>(list: T[]): T[] {
  return list.slice().sort((a, b) => time(b) - time(a) || a.session_id.localeCompare(b.session_id));
}

/** "Today" / "Yesterday" / "Last 7 days" / "Earlier" (and those without a time), newest first. */
export function groupByDay<T extends Session>(list: T[], now: Date): { title: string; items: T[] }[] {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1).getTime();
  const week = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 7).getTime();
  const out: { title: string; items: T[] }[] = [];
  for (const s of newestFirst(list)) {
    const at = time(s);
    const title = !at ? t("assistant-conversations.earlier") : at >= today ? t("assistant-conversations.today") : at >= yesterday ? t("assistant-conversations.yesterday") : at >= week ? t("assistant-conversations.last_7_days") : t("assistant-conversations.earlier");
    if (out.length && out[out.length - 1].title === title) out[out.length - 1].items.push(s);
    else out.push({ title, items: [s] });
  }
  return out;
}

/** One list from what was fetched with the archived ones (`fetched`), the store's list (newer conversations, without
 *  the archived ones) and the live sessions pushed since (state, title, last message, and the archive mark: the hub
 *  marks every assistant session it pushes, `archived` only when it is, so one without it was brought back since).
 *  Live sessions must belong to the selected manager and the main assistant space. */
export function mergeConversations(fetched: Conversation[], listed: Session[], live: Session[], manager?: string): Conversation[] {
  const by = new Map<string, Conversation>();
  for (const s of fetched) by.set(s.session_id, s);
  for (const s of listed) by.set(s.session_id, { ...by.get(s.session_id), ...s });
  for (const s of live) {
    if (!s.assistant || s.space || !sameManager(s.manager, manager)) continue;
    by.set(s.session_id, { ...by.get(s.session_id), ...s, archived: s.archived === true });
  }
  return [...by.values()];
}

/** The main conversation; the others (not archived), by day; the archived ones, newest first. */
export function arrange(list: Conversation[], main: string | undefined, now: Date) {
  const mainConv = list.find((s) => s.session_id === main && !s.archived);
  const rest = list.filter((s) => s.session_id !== mainConv?.session_id);
  return {
    main: mainConv,
    days: groupByDay(rest.filter((s) => !s.archived), now),
    archived: newestFirst(rest.filter((s) => s.archived)),
  };
}
