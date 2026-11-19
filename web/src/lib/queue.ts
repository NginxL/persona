// Messages typed while the agent works and not yet taken (Session.queued): "Pull back to edit" pulls them all back out of
// Claude and re-queues the others, in order (POST …/queue {keep}, node editQueue; iOS QueueStrip / editQueued).
import { t } from "../i18n";
import { quotePath } from "./attachments";
import { CONTEXT_RE } from "./compatibility";
import { parseUserText, tagAttrs, type AttachedFile } from "./turns";

/** The order to re-queue in when message `index` moves `by` places (−1 up, +1 down; #6088): every message, that one moved.
 *  Null when it cannot move that way. */
export function moved(count: number, index: number, by: number): number[] | null {
  return by === 0 ? null : movedTo(count, index, index + by);
}

/** The order to re-queue in when message `from` is put at place `to` (the queue's drag handle, dragged or ↑ ↓ Home End;
 *  #5327): every message, that one moved, the others in their order. Null when it stays or either place is outside. */
export function movedTo(count: number, from: number, to: number): number[] | null {
  if (from === to || from < 0 || from >= count || to < 0 || to >= count) return null;
  const order = Array.from({ length: count }, (_, i) => i);
  order.splice(from, 1);
  order.splice(to, 0, from);
  return order;
}

/** The indexes to re-queue when message `index` is taken out: every other one, in their order. */
export function keepAllBut(count: number, index: number): number[] {
  return Array.from({ length: count }, (_, i) => i).filter((i) => i !== index);
}

function normalize(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

/** Two texts are the same message once normalized, or one contains the other when both are long enough
 *  (Claude adds placeholders, the phone adds notes; a short "ok" must not match anything with "ok" in it). */
export function sameMessage(a: string, b: string): boolean {
  const x = normalize(a),
    y = normalize(b);
  if (!x || !y) return false;
  return x === y || (Math.min([...x].length, [...y].length) >= 8 && (x.includes(y) || y.includes(x)));
}

/** Photo-only messages sent from here (no words, files or contexts), paired one to one, in order, with the queued
 *  entries that hold as many photos and nothing else (#10871): Claude queues such a message as `[Image #n]` and the
 *  paths the node saved, which no text matches, so its sending bubble stayed in the transcript beside its queue row.
 *  Each entry pairs with at most one message and only by its real photo paths: a message or an entry left over on
 *  either side stays unpaired (its bubble shows). Returns the entry → the index of its message in `pending`. */
export function pairPhotoOnly(queued: string[], pending: { text: string; images: number; carries?: boolean }[]): Map<string, number> {
  const pairs = new Map<string, number>();
  const taken = new Set<number>();
  for (const raw of queued) {
    const q = parseQueued(raw);
    if (q.text || q.photos.length === 0 || q.files.length || q.contexts.length) continue;
    const i = pending.findIndex((p, k) => !taken.has(k) && !p.carries && !normalize(p.text) && p.images === q.photos.length);
    if (i < 0) continue;
    taken.add(i);
    pairs.set(raw, i);
  }
  return pairs;
}

/** A queued message read apart (#3032): Claude Code's queue holds only text, so what a message carried rides in it. */
export interface QueuedParts {
  /** What was typed, without Claude's `[Image #n]` placeholders, the tags or the stand-in words of a message with no words. */
  text: string;
  /** Paths, on the session's machine, of the photos it was sent with (`<attached_images>`). */
  photos: string[];
  /** Files it carries as <persona-file> tags (a video or a file sent from the phone). */
  files: AttachedFile[];
  /** Its <persona-context> blocks, every attribute kept (a workspace's machine and cwd). */
  contexts: QueuedContext[];
}

export interface QueuedContext {
  kind: string;
  title: string;
  url: string;
  text: string;
  /** Attributes after kind / title / url. */
  attrs?: Record<string, string>;
}

// The words the apps send in place of an empty message with photos, a video or contexts only, in every language they
// speak (iOS ChatView / AssistantView, Android ChatComposerState, web withContexts): data to match, not interface text.
const STAND_INS = [
  "Please look at the image",
  "Please look at the images",
  "Please look at the photos",
  "See the images",
  "See the context above.",
  "Refer to the content above.",
  "请看图片", // i18n-ignore
  "请看视频", // i18n-ignore
  "参考上面的内容。", // i18n-ignore
  "画像を見てください", // i18n-ignore
  "上の内容を参照してください。", // i18n-ignore
  "上の内容を参考にしてください。", // i18n-ignore
];

const IMAGES_BLOCK = /<attached_images>([\s\S]*?)<\/attached_images>/g;

/**
 * Reads a queued message apart, as Claude Code keeps it (made-up example):
 *
 *     [Image #1] [Image #2]
 *
 *     Please check these two screens
 *     <attached_images>
 *     path:/Users/me/.persona/uploads/2026-11-10/aaaa1111.jpg
 *     </attached_images>
 *
 * The node writes each photo's path twice, the second time after `path:` (node keepImagePaths); Claude's paste turns
 * the bare one into an `[Image #n]` and keeps the other. Nodes before #4361 wrote `path: ` with a space; older queues
 * have only the bare lines, or an empty block.
 * `photosBeside`: photos that came back beside the words (a Persona chat's, #4342), so stand-in words are dropped too.
 */
export function parseQueued(raw: string, photosBeside = 0): QueuedParts {
  const contexts: QueuedContext[] = [];
  let rest = raw.replace(CONTEXT_RE, (_, attrs: string, body: string) => {
    const { kind = "context", title = "", url = "", ...extra } = tagAttrs(attrs);
    if (kind === "skill-instructions") return "";
    contexts.push({ kind, title, url, text: body.trim(), ...(Object.keys(extra).length ? { attrs: extra } : {}) });
    return "";
  });
  rest = rest.replace(IMAGES_BLOCK, (_, body: string) => `<attached_images>${body.replace(/^\s*path: ?/gm, "")}</attached_images>`);
  const { text, files, photos } = parseUserText(rest);
  const carried = photosBeside + photos.length + files.length + contexts.length > 0;
  return { text: carried && isStandIn(text) ? "" : text, photos, files, contexts };
}

function isStandIn(text: string): boolean {
  const s = normalize(text);
  return STAND_INS.includes(s) || s === t("lib-links.see_images") || s === t("lib-links.see_context");
}

/** What a pulled-back message puts in the box: its words, then the path of each file that did not go back to its tile
 *  (lib/composer-files.ts restoreFiles). */
export function queuedDraft(p: Pick<QueuedParts, "text" | "files">): string {
  return [p.text, ...p.files.map((f) => quotePath(f.path))].filter(Boolean).join(" ");
}

/** How many of each kind of attachment a queued message carries, for its row's marks (only the kinds it has);
 *  `photosBeside` as in parseQueued. */
export function queuedMarks(p: QueuedParts, photosBeside = 0): { kind: "photo" | "video" | "file" | "context"; count: number }[] {
  const videos = p.files.filter((f) => f.kind === "video").length;
  const marks = [
    { kind: "photo" as const, count: p.photos.length + photosBeside },
    { kind: "video" as const, count: videos },
    { kind: "file" as const, count: p.files.length - videos },
    { kind: "context" as const, count: p.contexts.length },
  ];
  return marks.filter((m) => m.count > 0);
}
