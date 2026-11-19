// Paths a message names (docs/web/web.md "File paths in messages"): what looks like a file on the agent's machine, so the page
// can ask the machine which are there (lib/path-presence.ts) and link only those. The same rules as iOS's FilePaths,
// plus a line: path:12, path:12:5, path:12-20, path#L12.
//
// - ~/x, /x/y (two parts at least: /tmp alone is a word), or relative with a folder and an extension (src/a.ts; a
//   letter-led extension, so deepseek/v4.1 is not a file).
// - Its names may be in any script (Chinese, Japanese, accented). Outside backticks it ends at a space, at punctuation
//   other than _ . @ + - (full-width commas, full stops and brackets too), and before a Chinese word right after an
//   extension (/tmp/a.md followed by one); a relative one starts with an ASCII folder, so the words before it stay out.
//   A path in backticks is whole, whatever it is written in, spaces too: a home path whose folder and file names are Chinese.
// - Not inside a URL or a longer word; a sentence's full stop after it is not part of it.

export type PathRef = {
  /** Where it is in the text: [start, end) covers the path and its line. */
  start: number;
  end: number;
  /** The path as written, without the line (what fs.stat is asked about). */
  path: string;
  line?: number;
  col?: number;
};

// A name: ASCII, or a letter or digit of any script not right after an extension. The second branch never takes an ASCII
// letter or digit, so each character has one way to match: overlapping branches backtrack exponentially on a long
// extensionless part such as a 32-character hash (#8894).
const SEG = "(?:[A-Za-z0-9_.@+\\-]|(?<!\\.[A-Za-z][A-Za-z0-9]{0,7})(?![A-Za-z0-9])[\\p{L}\\p{M}\\p{N}])+";
const LINE = String.raw`(?::(\d{1,7})(?::(\d{1,5}))?(?:[-–]\d{1,7})?|#L(\d{1,7})(?:-L?\d{1,7})?)?`;
const TEXT = new RegExp(
  String.raw`(?<![A-Za-z0-9_/:.\-~])(~(?:/${SEG})+/?|/${SEG}(?:/${SEG})+/?|[A-Za-z0-9_.@\-]+(?:/${SEG})+\.[A-Za-z][A-Za-z0-9]{0,7})${LINE}(?![A-Za-z0-9_/\-])`,
  "gu",
);
const CODE_SPAN = /`([^`\n]+)`/g;
// A whole code span that is a path: ~/ or / then anything but a backtick (non-ASCII names too), and a line (the path
// takes as little as it can, so a line at the end is the line, not part of the name).
const WHOLE = new RegExp(String.raw`^(~?/[^\s\`]+?(?: [^\s\`]+?)*?)${LINE}$`);

/**
 * The paths in plain text, in order, at most `limit`. A code span is a candidate whole and also for the paths in it
 * (`/a.md`, the Chinese word for "and", `/b.md`): `splitAtRefs` takes the first that is there, the longer one at the same place.
 */
export function findPathRefs(text: string, limit = 40): PathRef[] {
  const out: PathRef[] = [];
  for (const m of text.matchAll(CODE_SPAN)) {
    const start = m.index + 1;
    const ref = codePathRef(m[1]);
    if (ref) out.push({ ...ref, start: start + ref.start, end: start + ref.end });
  }
  for (const m of text.matchAll(TEXT)) {
    const ref = fromMatch(m, m.index);
    // A whole code path owns its final dots; do not offer a different, shortened filename if it is missing.
    if (ref && !out.some((r) => r.start === ref.start && (r.end === ref.end || /^\.+$/.test(text.slice(ref.end, r.end))))) out.push(ref);
  }
  out.sort((a, b) => a.start - b.start || b.end - a.end);
  return out.slice(0, limit);
}

/** The path a whole code span is (`/tmp/a.md`, `src/a.ts:12`, a home path with Chinese folder and file names), or undefined. */
export function codePathRef(code: string): PathRef | undefined {
  const t = code.trim();
  if (!t || t.startsWith("//") || t.includes("://")) return undefined;
  const start = code.indexOf(t);
  const w = WHOLE.exec(t);
  if (w) {
    const ref = fromMatch(w, start, false);
    if (ref && ref.path.split("/").filter(Boolean).length >= (ref.path.startsWith("~") ? 1 : 2)) return ref;
  }
  const m = t.matchAll(TEXT).next().value;
  if (m?.[0] === t) return fromMatch(m, start);
  return undefined;
}

function fromMatch(m: RegExpMatchArray, at: number, trimPunctuation = true): PathRef | undefined {
  let path = m[1];
  const hasLine = m[0] !== path;
  // A sentence's full stop (or an ellipsis) is not part of the path, unless the path is only dots.
  if (!hasLine && trimPunctuation) path = path.replace(/(?<=[^./])\.+$/, "");
  if (!path || path === "~" || path === "/" || /^~?\/+$/.test(path)) return undefined;
  const num = (s?: string) => (s ? Number(s) : undefined);
  const line = num(m[2] ?? m[4]);
  return { start: at, end: at + (hasLine ? m[0].length : path.length), path, line: line || undefined, col: line ? num(m[3]) : undefined };
}

/** Each path once, in order, for asking the machine. */
export function uniquePaths(refs: PathRef[], limit = 24): string[] {
  const out: string[] = [];
  for (const r of refs) if (!out.includes(r.path) && out.length < limit) out.push(r.path);
  return out;
}

/** Text cut at the refs that `linkable` says yes to: strings between, refs at odd places. */
export function splitAtRefs(text: string, refs: PathRef[], linkable: (path: string) => boolean): (string | PathRef)[] {
  const out: (string | PathRef)[] = [];
  let at = 0;
  for (const r of refs) {
    if (r.start < at || !linkable(r.path)) continue;
    out.push(text.slice(at, r.start), r);
    at = r.end;
  }
  out.push(text.slice(at));
  return out;
}
