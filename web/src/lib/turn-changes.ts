// What a finished exchange's own edits changed, by file (#5157; drawn by views/chat/TurnChanges.tsx). Counted without
// building any diff: an edit's row draws its full diff only when opened (views/chat/ActionRow.tsx lineDiff, over the
// same lines, so the same numbers). The node counts a folded run the same way (server/internal/transcript/
// fold_changes.go), and both read spec/fixtures/fold-changes, so the card is the same before and after a reload.
import { relativeTo } from "./comments";
import { joinPath, normalizePath, toolFile } from "./files";
import { patchDiffs } from "./tool-detail";
import type { Step } from "./turns";
import type { Item } from "./types";

/** The calls that edit a file and have a diff (a Codex file change is an Edit with its `diff`). */
export const EDIT_TOOLS = new Set(["Edit", "Write", "MultiEdit"]);

export interface ChangedFile {
  /** As the edit named it, made absolute against the session's folder when it was relative, normalized. */
  path: string;
  /** Relative to the session's folder when inside it. */
  shown: string;
  add: number;
  del: number;
  /** False when the hub clipped one of its edits' text for good: its counts would be a guess. */
  exact: boolean;
}

export interface TurnChangeSet {
  files: ChangedFile[];
  add: number;
  del: number;
  /** Every file's counts are known. */
  exact: boolean;
}

interface Counts {
  add: number;
  del: number;
}

/** An edit's two texts as lines. A newline at a text's end ends its last line rather than starting another, on a side
 *  whose other side is empty or ends in a newline too: a Write of "a\nb\n" is two lines, and "foo" becoming "foo\n" is
 *  still a change (the node splits the same way, fold_changes.go editLines). */
export function editLines(oldText: string, newText: string): [string[], string[]] {
  const oldEnds = oldText.endsWith("\n");
  const newEnds = newText.endsWith("\n");
  const split = (s: string, strip: boolean) => (s ? (strip ? s.slice(0, -1) : s).split("\n") : []);
  return [split(oldText, oldEnds && (!newText || newEnds)), split(newText, newEnds && (!oldText || oldEnds))];
}

/** The fewest lines to remove and add to make `a` into `b` (Myers' forward pass, no trace kept), or null past
 *  lib/compare.ts diffLines' limits, where the diff draws everything removed and everything added. */
export function editDistance(a: readonly string[], b: readonly string[], limit = 60_000, maxEdits = 6_000): number | null {
  const n = a.length;
  const m = b.length;
  if (n + m > limit) return null;
  const max = Math.min(n + m, maxEdits);
  const off = max + 1;
  const v = new Int32Array(2 * max + 3);
  for (let d = 0; d <= max; d++) {
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[off + k - 1] < v[off + k + 1]) ? v[off + k + 1] : v[off + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[off + k] = x;
      if (x >= n && y >= m) return d;
    }
  }
  return null;
}

/** The lines added and removed between two texts, as their line diff draws them. */
export function lineCounts(oldText: string, newText: string): Counts {
  const [a, b] = editLines(oldText, newText);
  const d = editDistance(a, b);
  return d === null ? { add: b.length, del: a.length } : { add: (b.length - a.length + d) / 2, del: (a.length - b.length + d) / 2 };
}

/** The lines a Codex file change's unified diff adds and removes, read as its row reads them (lib/tool-detail.ts
 *  patchDiffs: the lines in its hunks, or the whole file without hunks). A patch is no Myers diff: nothing to save. */
export function patchCounts(patch: string, change?: string): Counts {
  const n = { add: 0, del: 0 };
  for (const d of patchDiffs(patch, change)) for (const o of d) if (o.kind !== "same") n[o.kind]++;
  return n;
}

const text = (v: unknown) => String(v ?? "");

/** The lines an edit call adds and removes (the numbers its row shows, views/chat/ActionRow.tsx editDiffs). */
export function editCounts(use: Item): Counts {
  const inp = (use.tool_input ?? {}) as Record<string, unknown>;
  switch (use.tool_name) {
    case "Edit":
      if (typeof inp.diff === "string") return patchCounts(inp.diff, typeof inp.change === "string" ? inp.change : undefined);
      return typeof inp.old_string === "string" || typeof inp.new_string === "string" ? lineCounts(text(inp.old_string), text(inp.new_string)) : { add: 0, del: 0 };
    case "Write":
      return typeof inp.content === "string" ? lineCounts("", inp.content) : { add: 0, del: 0 };
    case "MultiEdit": {
      const sum = { add: 0, del: 0 };
      if (Array.isArray(inp.edits))
        for (const e of inp.edits as Record<string, unknown>[]) {
          const n = lineCounts(text(e?.old_string), text(e?.new_string));
          sum.add += n.add;
          sum.del += n.del;
        }
      return sum;
    }
  }
  return { add: 0, del: 0 };
}

/** The note the hub ends a tool input's string with when it clipped it for good (server/internal/transcript/clip.go
 *  clipNoteFormat). */
const CLIPPED = /\n…（已截断，共 ≈?\d+ 字节）$/; // i18n-ignore: matches the hub's text

function clipped(v: unknown): boolean {
  if (typeof v === "string") return CLIPPED.test(v);
  if (Array.isArray(v)) return v.some(clipped);
  return Boolean(v) && typeof v === "object" && Object.values(v as Record<string, unknown>).some(clipped);
}

const absolute = (p: string) => p.startsWith("/") || p.startsWith("~") || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith("\\\\");

/** An edit's path, normalized against the session's folder when it was relative. */
export function changePath(named: string, cwd?: string): string {
  return normalizePath(cwd && !absolute(named) ? joinPath(cwd, named) : named);
}

/** What an exchange's own edits changed, by file in the order first edited, or null when there is nothing to show (no
 *  successful edit). Work the hub folded counts by its fold's `changes`; a fold without them (an older node) leaves the
 *  edits unknown, and there is no card rather than a wrong count. `steps` are the exchange's own (`exchangeSteps`),
 *  never its subagents'. */
export function turnChanges(steps: readonly Step[], cwd?: string): TurnChangeSet | null {
  const files = new Map<string, ChangedFile>();
  const add = (named: string, n: Counts, exact: boolean) => {
    const path = changePath(named, cwd);
    const had = files.get(path);
    if (had) {
      had.add += n.add;
      had.del += n.del;
      had.exact &&= exact;
    } else files.set(path, { path, shown: relativeTo(cwd, path), add: n.add, del: n.del, exact });
  };
  for (const s of steps) {
    if (s.kind === "fold") {
      const changes = s.item.fold?.changes;
      if (!Array.isArray(changes)) return null;
      for (const c of changes) if (c.path) add(c.path, c, c.exact !== false);
      continue;
    }
    if (s.kind !== "tool") continue;
    const name = s.use.tool_name ?? "";
    if (!EDIT_TOOLS.has(name) || !s.result || s.result.is_error) continue;
    const f = toolFile(name, s.use.tool_input);
    if (f) add(f.path, editCounts(s.use), !clipped(s.use.tool_input));
  }
  if (!files.size) return null;
  const list = [...files.values()];
  return { files: list, add: list.reduce((a, f) => a + f.add, 0), del: list.reduce((a, f) => a + f.del, 0), exact: list.every((f) => f.exact) };
}
