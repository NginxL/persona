import { fenced } from "./chat-export";
import type { Star, StarInput } from "./starred-rows";

export const STARRED_BACKUP_MAX_BYTES = 8 * 1024 * 1024;
const BACKUP_FORMAT = "persona.starred";
const BACKUP_VERSION = 1;
const ARCHIVE_MACHINE = "persona-starred-backup";
const ARCHIVE_SESSION = "backup.";
const ARCHIVE_ITEM = "line0#0";
const KEY = /^sm[0-9a-f]{32}$/;

export interface StarredBackupMessage extends Pick<StarInput, "conversation" | "role" | "title" | "speaker" | "text" | "written_at" | "omitted_chars"> {
  key: string;
  starred_at: string;
}

export interface StarredBackup {
  format: typeof BACKUP_FORMAT;
  version: typeof BACKUP_VERSION;
  messages: StarredBackupMessage[];
}

/** A backup owns a saved snapshot only. Its synthetic identity is never a link into a transcript. */
export function restoredStarKey(star: Star): string | undefined {
  if (star.machine_id !== ARCHIVE_MACHINE || star.item_id !== ARCHIVE_ITEM || star.source_fingerprint || star.source_manager ||
      !star.session_id.startsWith(ARCHIVE_SESSION)) return;
  const key = star.session_id.slice(ARCHIVE_SESSION.length);
  return KEY.test(key) ? key : undefined;
}

/** Project each snapshot explicitly: never copy IDs of nodes, sessions, managers or transcript items. */
export function starredBackup(stars: readonly Star[]): StarredBackup {
  return {
    format: BACKUP_FORMAT,
    version: BACKUP_VERSION,
    messages: stars.map((star) => {
      const { conversation, role, title, speaker, text, starred_at, written_at, omitted_chars } = star;
      return { key: restoredStarKey(star) ?? star.id, conversation, role, title, speaker, text, starred_at, written_at, omitted_chars };
    }),
  };
}

const STRING_FIELDS = ["key", "title", "speaker", "text", "starred_at"] as const;
const MESSAGE_FIELDS = new Set<string>([...STRING_FIELDS, "conversation", "role", "written_at", "omitted_chars"]);
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

function isMessage(value: unknown): value is StarredBackupMessage {
  return object(value) && Object.keys(value).every((key) => MESSAGE_FIELDS.has(key)) &&
    STRING_FIELDS.every((key) => typeof value[key] === "string") &&
    typeof value.key === "string" && KEY.test(value.key) &&
    (value.conversation === "assistant" || value.conversation === "agent") &&
    (value.role === "user" || value.role === "assistant") &&
    (value.written_at === undefined || typeof value.written_at === "string") &&
    (value.omitted_chars === undefined || typeof value.omitted_chars === "number" && Number.isSafeInteger(value.omitted_chars) && value.omitted_chars >= 0);
}

/** Do not send extra keys or arbitrary JSON selected by mistake. The manager checks snapshot limits and identity. */
export function parseStarredBackup(raw: string): StarredBackup | undefined {
  try {
    const file: unknown = JSON.parse(raw);
    if (!object(file) || file.format !== BACKUP_FORMAT || file.version !== BACKUP_VERSION ||
        Object.keys(file).length !== 3 || !Array.isArray(file.messages) || !file.messages.every(isMessage)) return;
    return { format: BACKUP_FORMAT, version: BACKUP_VERSION, messages: file.messages };
  } catch {
    return undefined;
  }
}

/** Keep the preview request's input intact. Reserializing JSON would hide duplicate keys from the manager. */
export function decodeStarredBackup(bytes: ArrayBuffer): { raw: string; bundle: StarredBackup } | undefined {
  try {
    const raw = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes);
    const bundle = parseStarredBackup(raw);
    return bundle && { raw, bundle };
  } catch {
    return undefined;
  }
}

const inline = (text: string) => text.trim().replace(/\s+/gu, " ").replace(/[\\`*_~\[\]<>]/g, "\\$&");

export function starredMarkdown(stars: readonly Star[], words: {
  title: string;
  conversation: string;
  speaker: (star: Star) => string;
  omitted: (count: number) => string;
}): string {
  const sections = [`# ${inline(words.title)}`];
  for (const star of stars) {
    const details = [inline(words.speaker(star)), inline(star.written_at || star.starred_at)].filter(Boolean).join(" · ");
    const clipped = star.omitted_chars ? `\n\n${inline(words.omitted(star.omitted_chars))}` : "";
    sections.push(`## ${inline(star.title || words.conversation)}\n\n${details}\n\n${fenced(star.text)}${clipped}`);
  }
  return sections.join("\n\n") + "\n";
}
